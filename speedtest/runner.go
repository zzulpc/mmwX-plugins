package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	defaultTestURL      = "https://dl.google.com/dl/android/studio/install/3.4.1.0/android-studio-ide-183.5522156-windows.exe"
	defaultTestDuration = 8 * time.Second
	latencyProbeURL     = "https://www.gstatic.com/generate_204"
	cfLatencyProbeURL   = "https://cp.cloudflare.com/generate_204" // 真延迟用 Cloudflare 204(全球边缘 + CDN 边)
	egressIPProbeURL    = "https://api.ipify.org"                  // 经代理回显出口 IP,用于核对出站链路是否符合预期
	cfLatencySamples    = 3                                        // 真延迟采样次数,取最快 2 个均值(去掉首包冷启动)
)

// 单次测速各阶段的超时上限,以及必须能把它们全部装下的执行预算。
//
// 这笔加法以前没人做过:任务预算写死成 defaultTestDuration+30s = 38s,而各阶段上限加起来是
// 15(内核就绪)+10(sing-box check)+8(出口 IP)+10(延迟)+10(下载准备)+8(吞吐)= 51~61s。
// 结果是「慢但能用」的节点走到下载阶段时剩余时间已经不够,报出
// 「测速任务剩余时间不足以完成 8s 吞吐测试」—— 看着像节点坏了,其实是自家预算算错了。
// TestRun执行预算能装下所有阶段超时 把这笔加法钉死:改任何一个阶段超时都必须同步过它。
const (
	egressProbeTimeout     = 5 * time.Second  // 出口 IP 回显
	latencyProbeTimeout    = 5 * time.Second  // 普通 204 延迟探测
	cfLatencySampleTimeout = 4 * time.Second  // LatencyOnly 单次采样
	cfLatencyTotalTimeout  = 12 * time.Second // LatencyOnly 整个采样阶段(3 次采样共用)
	downloadSetupTime      = 10 * time.Second // 首个 2xx 的独立准备预算,不挤占吞吐窗口
	runPhaseMargin         = 5 * time.Second  // 进程启停、临时目录、调度等零碎开销

	// runExecutionBudget 只从任务取得执行权之后开始走,不含排队等待 —— 排队预算见 main.go
	// 的 runQueueWaitBudget。两者以前混在一起,4 个在途任务共用同一个 38s 时钟,
	// 排在后面的任务会被前面的执行时间吃光预算。
	runExecutionBudget = coreReadyTimeout + singBoxCheckTimeout + egressProbeTimeout +
		latencyProbeTimeout + downloadSetupTime + defaultTestDuration + runPhaseMargin
)

// runExecutionSlot 串行化测速，同时允许排队任务因超时或断线立即退出。
var runExecutionSlot = make(chan struct{}, 1)

// beginRun 先等待执行权，再从父上下文派生独立的执行时钟；排队期限不能成为执行期限的父级。
func beginRun(ctx context.Context, queueDeadline time.Time, executionBudget time.Duration) (context.Context, func(), error) {
	queueCtx, cancelQueue := context.WithDeadline(ctx, queueDeadline)
	defer cancelQueue()
	select {
	case runExecutionSlot <- struct{}{}:
		// 取消与空闲槽同时就绪时，select 可能选中槽位，仍须拒绝已过期的任务。
		if err := queueCtx.Err(); err != nil {
			<-runExecutionSlot
			return nil, nil, fmt.Errorf("测速任务等待执行超时: %w", err)
		}
	case <-queueCtx.Done():
		return nil, nil, fmt.Errorf("测速任务等待执行超时: %w", queueCtx.Err())
	}
	if executionBudget <= 0 {
		executionBudget = runExecutionBudget
	}
	executionCtx, cancelExecution := context.WithTimeout(ctx, executionBudget)
	return executionCtx, func() {
		cancelExecution()
		<-runExecutionSlot
	}, nil
}

// Result 单节点测速结果。
type Result struct {
	DownMbps  float64
	LatencyMs int64
	Bytes     int64
	Duration  time.Duration
	EgressIP  string
}

// Options 测速参数(留空用默认)。
type Options struct {
	TestURL      string        // 测试下载 URL(默认大文件)
	TestDuration time.Duration // 测速时长(默认 8s):下载这么久,按真实字节/耗时算速率
	TestBytes    int64         // 可选下载上限(0=不限,纯按时长)
	Timeout      time.Duration
	Threads      int  // 并发下载线程数(<=1 单线程)
	BufSize      int  // 每次收发的 io/socket buffer 字节数(默认 1MB;clamp 见 clampSpeedTestParams)
	LatencyOnly  bool // true 仅测真连接延迟(Cloudflare 204 多采样)不跑大文件下载
}

// 测速 buffer / 线程的取值边界。峰值内存 ≈ BufSize × Threads × downloadBuffersPerThread,
// maxSpeedTotalMem 防家用测速端 OOM。
const (
	defaultBufSize   = 1 << 20   // 1MB(= 历史固定值,向后兼容)
	minBufSize       = 64 << 10  // 64KB
	maxBufSize       = 16 << 20  // 16MB
	maxSpeedThreads  = 64        // 并发下载线程上限
	maxSpeedTotalMem = 256 << 20 // 峰值内存上限:超了缩 BufSize
	// downloadBuffersPerThread:每条下载流实际占两份 bufSize —— io.CopyBuffer 的 buffer 一份,
	// 该线程自己那个 http.Transport 的 ReadBufferSize 又一份(见 newProxyTransport)。
	// 原来只按一份算,64 线程 × 4MB 实际吃掉 ~512MB,是这个 256MB 闸门的两倍。
	downloadBuffersPerThread = 2
)

var (
	errDownloadWindowExpired = errors.New("下载测速窗口已结束")
	errDownloadQuotaReached  = errors.New("下载字节额度已用尽")
	errDownloadSetupExpired  = errors.New("等待首个下载响应超时")
)

// clampSpeedTestParams 归一 bufSize(字节)与 threads,并把峰值内存收敛到 maxSpeedTotalMem 内。
// 峰值按 bufSize × threads × downloadBuffersPerThread 算,不是只算 copy buffer 那一份。
// 0/越界回落默认(bufSize=1MB, threads=1)。
func clampSpeedTestParams(bufSize, threads int) (int, int) {
	if threads <= 0 {
		threads = 1
	}
	if threads > maxSpeedThreads {
		threads = maxSpeedThreads
	}
	if bufSize <= 0 {
		bufSize = defaultBufSize
	}
	if bufSize < minBufSize {
		bufSize = minBufSize
	}
	if bufSize > maxBufSize {
		bufSize = maxBufSize
	}
	if int64(bufSize)*int64(threads)*downloadBuffersPerThread > maxSpeedTotalMem {
		bufSize = int(int64(maxSpeedTotalMem) / (int64(threads) * downloadBuffersPerThread))
		if bufSize < minBufSize {
			bufSize = minBufSize
		}
	}
	return bufSize, threads
}

// RunNodeTest 用已选择的代理内核启动单节点代理，并测量延迟、出口 IP 与下行吞吐。
func RunNodeTest(ctx context.Context, runtimeInfo proxyRuntime, clashConfigJSON string, opts Options) (Result, error) {
	executionCtx, finish, err := beginRun(ctx, time.Now().Add(runQueueWaitBudget), opts.Timeout)
	if err != nil {
		return Result{}, err
	}
	defer finish()
	return runNodeTest(executionCtx, runtimeInfo, clashConfigJSON, opts)
}

// runNodeTest 只供已取得执行权的入口调用，主控任务的内核准备与测速共用一份执行预算。
func runNodeTest(ctx context.Context, runtimeInfo proxyRuntime, clashConfigJSON string, opts Options) (Result, error) {
	if opts.TestDuration <= 0 {
		opts.TestDuration = defaultTestDuration
	}

	testURL := opts.TestURL
	if testURL == "" {
		testURL = defaultTestURL // 固定大文件,下载满测速时长即停
	}

	proxy, err := parseClashProxy(clashConfigJSON)
	if err != nil {
		return Result{}, err
	}
	selectedCore, err := selectProxyCore(proxy)
	if err != nil {
		return Result{}, err
	}
	if selectedCore != runtimeInfo.Core {
		return Result{}, fmt.Errorf("节点需要 %s，但任务准备的是 %s", selectedCore, runtimeInfo.Core)
	}
	mixedPort, err := reserveLoopbackPort()
	if err != nil {
		return Result{}, err
	}
	var (
		config     []byte
		configName string
	)
	switch runtimeInfo.Core {
	case coreSingBox:
		config, err = buildSingBoxConfig(proxy, mixedPort)
		configName = "config.json"
	case coreMihomo:
		config, err = buildMihomoConfig(proxy, mixedPort)
		configName = "config.yaml"
	default:
		return Result{}, fmt.Errorf("未知代理内核: %s", runtimeInfo.Core)
	}
	if err != nil {
		return Result{}, err
	}
	workdir, err := os.MkdirTemp("", "mmwx-speedtest-")
	if err != nil {
		return Result{}, fmt.Errorf("创建测速临时目录失败: %w", err)
	}
	defer func() {
		if cleanupErr := os.RemoveAll(workdir); cleanupErr != nil {
			log.Printf("[warn] 清理测速临时目录失败 %s: %v", workdir, cleanupErr)
		}
	}()
	if err := os.Chmod(workdir, 0700); err != nil {
		return Result{}, fmt.Errorf("收紧测速临时目录权限失败: %w", err)
	}
	configPath := filepath.Join(workdir, configName)
	if err := os.WriteFile(configPath, config, 0600); err != nil {
		return Result{}, fmt.Errorf("写入临时代理配置失败: %w", err)
	}
	stop, err := startProxyCore(ctx, runtimeInfo, workdir, configPath, collectProxySecrets(proxy), mixedPort)
	if err != nil {
		return Result{}, err
	}
	defer stop()

	egressIP := measureEgressIP(ctx, mixedPort)

	// LatencyOnly:只测真连接延迟(Cloudflare 204 多采样),不跑下载
	if opts.LatencyOnly {
		latency := measureLatencyCloudflare(ctx, mixedPort, cfLatencySamples)
		result := Result{LatencyMs: latency, EgressIP: egressIP}
		// 没有有效样本或任务已经取消时必须回报失败，否则主控会把 -1 毫秒当成成功结果。
		if err := ctx.Err(); err != nil {
			return result, fmt.Errorf("延迟测速被取消: %w", err)
		}
		if latency < 0 {
			return result, fmt.Errorf("延迟测速失败: 未获得有效延迟样本")
		}
		return result, nil
	}

	latency := measureLatency(ctx, mixedPort)

	bufSize, threads := clampSpeedTestParams(opts.BufSize, opts.Threads)
	n, dur, err := downloadTimed(ctx, testURL, opts.TestDuration, opts.TestBytes, threads, bufSize, mixedPort)
	if err != nil {
		return Result{LatencyMs: latency, EgressIP: egressIP}, fmt.Errorf("下载测速失败: %w", err)
	}
	mbps := 0.0
	if dur > 0 {
		mbps = float64(n) * 8 / dur.Seconds() / 1e6
	}
	return Result{DownMbps: mbps, LatencyMs: latency, Bytes: n, Duration: dur, EgressIP: egressIP}, nil
}

// 生成配置里固定使用的节点名与分组名。
//
// 绝不能沿用主控下发的节点名:它来自订阅、由用户可控。撞上 mihomo 预置的 DIRECT / REJECT /
// GLOBAL / PASS / COMPATIBLE,或者撞上下面这个分组名,mihomo 都会以「名字重复」直接拒绝
// 加载配置 —— 用户只看得到一句「mihomo 在本地代理端口就绪前退出」,完全指不到根因。
// 这个名字只在临时配置内部用于把 rules 指向唯一那个出站,不参与任何对外展示。
const (
	mihomoNodeTag  = "node-under-test"
	mihomoGroupTag = "MMWX-SPEEDTEST"
)

// buildMihomoConfig 保留原有单节点 Clash 配置路径，仅把本地端口改为每任务动态分配。
func buildMihomoConfig(proxy map[string]any, mixedPort int) ([]byte, error) {
	normalizedProxy, ok := normalizeJSONNumbers(proxy).(map[string]any)
	if !ok {
		return nil, fmt.Errorf("规范化 Mihomo 节点配置失败")
	}
	normalizedProxy["name"] = mihomoNodeTag
	mini := map[string]any{
		"mixed-port": mixedPort,
		"allow-lan":  false,
		"mode":       "rule",
		"log-level":  "warning",
		// 不开 external-controller:测速流程一次都没用到它,而它是个无鉴权的管理接口,
		// 同机上的其它用户在测速窗口内可以拿它改配置、读连接表。
		"proxies": []map[string]any{normalizedProxy},
		"proxy-groups": []map[string]any{
			{"name": mihomoGroupTag, "type": "select", "proxies": []string{mihomoNodeTag}},
		},
		"rules": []string{"MATCH," + mihomoGroupTag},
	}
	config, err := yaml.Marshal(mini)
	if err != nil {
		return nil, fmt.Errorf("生成 Mihomo 配置失败: %w", err)
	}
	return config, nil
}

// reserveLoopbackPort 为单次任务分配随机回环端口，避免旧孤儿进程造成假就绪。
func reserveLoopbackPort() (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("分配本地代理端口失败: %w", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		return 0, fmt.Errorf("释放本地代理预留端口失败: %w", err)
	}
	return port, nil
}

// measureEgressIP 经代理请求一个 IP 回显端点,拿到出口 IP;失败返回空。
func measureEgressIP(ctx context.Context, mixedPort int) string {
	client := proxyClient(mixedPort)
	defer client.CloseIdleConnections()
	client.Timeout = egressProbeTimeout
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, egressIPProbeURL, nil)
	if err != nil {
		return ""
	}
	resp, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return ""
	}
	buf, err := io.ReadAll(io.LimitReader(resp.Body, 64))
	if err != nil {
		return ""
	}
	ip := strings.TrimSpace(string(buf))
	if len(ip) < 3 || len(ip) > 45 || (!strings.Contains(ip, ".") && !strings.Contains(ip, ":")) {
		return ""
	}
	return ip
}

// proxyClient 经本次任务的 mixed 入站走代理。每次调用都新建一个 Transport(短探测各自独立,
// 且必须能各自 CloseIdleConnections),不是共享的。
// 单流测速调优:1MB ReadBufferSize / 禁 HTTP/2(单流被流控限速)/ 禁 Compression
func proxyClient(mixedPort int) *http.Client {
	return &http.Client{Transport: newProxyTransport(mixedPort, defaultBufSize)}
}

// newProxyTransport 构造经本次任务 mixed 入站的 Transport。readBuf 决定 socket 读缓冲(降 read syscall 频率);
// 禁 HTTP/2(单流被流控限速)、禁压缩。下载测速按用户选的 bufSize per-call 构造。
func newProxyTransport(mixedPort, readBuf int) *http.Transport {
	proxyURL, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", mixedPort))
	if readBuf < minBufSize {
		readBuf = minBufSize
	}
	return &http.Transport{
		Proxy:              http.ProxyURL(proxyURL),
		ReadBufferSize:     readBuf,
		WriteBufferSize:    64 << 10,
		DisableCompression: true,
		ForceAttemptHTTP2:  false,
		TLSNextProto:       map[string]func(string, *tls.Conn) http.RoundTripper{}, // 显式禁 HTTP/2
		MaxIdleConns:       64,
		IdleConnTimeout:    90 * time.Second,
	}
}

// proxyClientBuf 下载测速用:按 bufSize 配 socket 读缓冲。
func proxyClientBuf(mixedPort, bufSize int) *http.Client {
	return &http.Client{Transport: newProxyTransport(mixedPort, bufSize)}
}

// getCopyBuf/putCopyBuf 自适应 io.CopyBuffer 缓冲池:cap>=size 复用,否则新建。
// 支持用户选的 1/4/8/16M 包大小(峰值内存 ≈ bufSize×threads,已由 clampSpeedTestParams 收敛)。
var copyBufPool sync.Pool

func getCopyBuf(size int) *[]byte {
	if v := copyBufPool.Get(); v != nil {
		b := v.(*[]byte)
		if cap(*b) >= size {
			*b = (*b)[:size]
			return b
		}
	}
	b := make([]byte, size)
	return &b
}

func putCopyBuf(b *[]byte) { copyBufPool.Put(b) }

// measureLatency 经代理 GET 一个 204 端点,返回毫秒;失败返回 -1。
//
// 固定端点只认 204；登录页或错误页即使返回 200，也不能作为有效延迟。
func measureLatency(ctx context.Context, mixedPort int) int64 {
	client := latencyProbeClient(mixedPort, latencyProbeTimeout)
	defer client.CloseIdleConnections()
	return measureLatencySample(ctx, client, latencyProbeURL)
}

// 固定探测端点不应跳转；跟随登录页后再收到 204 也不能证明原端点正常。
func latencyProbeClient(mixedPort int, timeout time.Duration) *http.Client {
	client := proxyClient(mixedPort)
	client.Timeout = timeout
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return client
}

// 两种延迟模式共用相同的响应校验，避免多采样路径重新接受错误页。
func measureLatencySample(ctx context.Context, client *http.Client, probeURL string) int64 {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, probeURL, nil)
	if err != nil {
		return -1
	}
	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return -1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return -1
	}
	// 正常 204 没有响应体；只读一个字节即可拒绝异常内容，并保留读取失败和取消。
	n, err := io.Copy(io.Discard, io.LimitReader(resp.Body, 1))
	if err != nil || n != 0 || ctx.Err() != nil {
		return -1
	}
	return time.Since(start).Milliseconds()
}

// measureLatencyCloudflare 用 Cloudflare 204 多次采样,取最快 2 个均值;
// 首包受 TLS 握手 / mihomo cold-start 影响,平均后更接近"真连接延迟"。全部失败返回 -1。
//
// 整个采样阶段另有 cfLatencyTotalTimeout 兜底:单次超时 × 采样数会突破 runExecutionBudget
// 里为这一阶段留的份额,而 LatencyOnly 任务后面还要靠这个预算收尾。
// 非 204 的样本直接丢弃，一个都没有就返回 -1，由调用方回报失败。
func measureLatencyCloudflare(ctx context.Context, mixedPort, samples int) int64 {
	if samples <= 0 {
		samples = cfLatencySamples
	}
	phaseCtx, cancelPhase := context.WithTimeout(ctx, cfLatencyTotalTimeout)
	defer cancelPhase()
	client := latencyProbeClient(mixedPort, cfLatencySampleTimeout)
	defer client.CloseIdleConnections()
	return measureLatencySamples(phaseCtx, client, cfLatencyProbeURL, samples)
}

// 汇总时只保留通过同一端点校验的样本；无有效样本仍须向调用方返回失败。
func measureLatencySamples(ctx context.Context, client *http.Client, probeURL string, samples int) int64 {
	probes := make([]int64, 0, samples)
	for i := 0; i < samples; i++ {
		if ctx.Err() != nil {
			break
		}
		if latency := measureLatencySample(ctx, client, probeURL); latency >= 0 {
			probes = append(probes, latency)
		}
	}
	if len(probes) == 0 {
		return -1
	}
	sortInt64Asc(probes)
	keep := 2
	if len(probes) < keep {
		keep = len(probes)
	}
	var sum int64
	for i := 0; i < keep; i++ {
		sum += probes[i]
	}
	return sum / int64(keep)
}

func sortInt64Asc(a []int64) {
	for i := 1; i < len(a); i++ {
		for j := i; j > 0 && a[j] < a[j-1]; j-- {
			a[j], a[j-1] = a[j-1], a[j]
		}
	}
}

// sharedDownloadQuota 在多个下载线程之间预留并结算同一份字节额度。
// 预留动作只短暂持锁，实际网络读取仍并发；已读取总量用原子计数，保证不会超出任务级上限。
type sharedDownloadQuota struct {
	maxBytes int64
	total    atomic.Int64
	mu       sync.Mutex
	reserved int64
	cancel   context.CancelCauseFunc
}

func (q *sharedDownloadQuota) reserve(size int) int {
	q.mu.Lock()
	defer q.mu.Unlock()
	remaining := q.maxBytes - q.total.Load() - q.reserved
	if remaining <= 0 {
		return 0
	}
	if int64(size) > remaining {
		size = int(remaining)
	}
	q.reserved += int64(size)
	return size
}

func (q *sharedDownloadQuota) finish(reserved, read int) {
	q.mu.Lock()
	q.reserved -= int64(reserved)
	total := q.total.Add(int64(read))
	reached := total >= q.maxBytes
	q.mu.Unlock()
	if reached {
		q.cancel(errDownloadQuotaReached)
	}
}

// sharedQuotaReader 在读取响应体之前先抢占额度，避免多个线程同时读过上限后才截断统计值。
type sharedQuotaReader struct {
	source io.Reader
	quota  *sharedDownloadQuota
}

func (r *sharedQuotaReader) Read(p []byte) (int, error) {
	reserved := r.quota.reserve(len(p))
	if reserved == 0 {
		// 额度可能已被其它并发读取预留但尚未结算；用专用终止原因避免把正常竞争误记为空响应。
		return 0, errDownloadQuotaReached
	}
	n, err := r.source.Read(p[:reserved])
	r.quota.finish(reserved, n)
	return n, err
}

func downloadTimed(ctx context.Context, dlURL string, dur time.Duration, maxBytes int64, threads, bufSize, mixedPort int) (int64, time.Duration, error) {
	if threads <= 1 {
		return downloadSingleTimed(ctx, dlURL, dur, maxBytes, bufSize, mixedPort)
	}

	// 多线程也必须走 downloadWindow。以前这里直接 WithTimeoutCause(ctx, dur) 并从协程起飞
	// 就开始计时:代理握手 + TLS + TTFB 全被算进了速率的分母,而字节要等 2xx 之后才进账。
	// 实测 setup 400ms / 窗口 800ms 时低估 50%,生产参数(8s 窗口、远端节点)低估 10~25%,
	// 延迟越高压得越狠 —— 恰好把用户最想横向比较的远端节点系统性地报低。
	// 单线程路径 v0.2.3 就修过同一个问题,多线程当时漏掉了。
	dlCtx, window, err := newDownloadWindow(ctx, dur)
	if err != nil {
		return 0, 0, err
	}
	defer window.stop()

	var wg sync.WaitGroup
	results := make([]int64, threads)
	errs := make([]error, threads)
	var quota *sharedDownloadQuota
	if maxBytes > 0 {
		quota = &sharedDownloadQuota{maxBytes: maxBytes, cancel: window.cancel}
	}
	for i := range threads {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			// 最先拿到 2xx 的那条流启动窗口。后到的流少测一点,这是多线程固有的,
			// 但比把每条流的 setup 都计进分母小一个数量级。
			n, _, _, _, e := downloadSingleAttempt(dlCtx, dlURL, maxBytes, bufSize, mixedPort, quota, window.startMeasurement)
			results[idx] = n
			errs[idx] = e
		}(i)
	}
	wg.Wait()
	elapsed := window.elapsed()
	if err := ctx.Err(); err != nil {
		return 0, elapsed, err
	}
	if window.setupExpired.Load() {
		// 一条流都没等到 2xx。此时所有 errs 都是被 setup 取消后的 context.Canceled,
		// 报窗口相关的错会误导排查方向,直接说清楚是卡在响应之前。
		return 0, elapsed, fmt.Errorf("等待下载响应超时: %d 条流均未收到有效响应", threads)
	}

	var total int64
	var firstErr error
	validResult := false
	failedStreams := 0
	stopCause := context.Cause(dlCtx)
	for i := range threads {
		total += results[i]
		expectedStop := isExpectedDownloadStop(stopCause, errs[i])
		if results[i] > 0 && (errs[i] == nil || expectedStop) {
			validResult = true
		}
		if errs[i] != nil && !expectedStop && firstErr == nil {
			firstErr = errs[i]
		}
		if errs[i] != nil && !expectedStop {
			failedStreams++
		}
	}
	// 过去只要任一路读到过字节就吞掉所有错误，会把 8 路全部提前断流误报为成功。
	// 至少一条流必须正常结束、达到共享额度或持续到测速期限，才能把总字节作为有效结果。
	if validResult {
		if failedStreams > 0 {
			log.Printf("[warn] 多线程下载有 %d/%d 条流提前失败，测速结果仅统计实际收到的字节", failedStreams, threads)
		}
		return total, elapsed, nil
	}
	if firstErr == nil {
		firstErr = fmt.Errorf("下载响应未包含数据")
	}
	return 0, elapsed, fmt.Errorf("0/%d 条下载流有效，%d 条失败，首个错误: %w", threads, failedStreams, firstErr)
}

// isExpectedDownloadStop 只把本测速函数自己的时长或字节额度取消视为正常结束；
// 父任务超时仍必须向上返回，不能因为已经收到部分字节就伪装成成功。
func isExpectedDownloadStop(cause, err error) bool {
	if !errors.Is(cause, errDownloadWindowExpired) && !errors.Is(cause, errDownloadQuotaReached) {
		return false
	}
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// downloadWindow 把“等待首个有效响应”和“吞吐计时”分开:计时只从第一个 2xx 开始走,
// 代理握手 / TLS / TTFB 落在独立的 setup 预算里,不进吞吐速率的分母。
// 单线程下同一个窗口最多容纳三次串行请求，断流重试不会额外获得一份测速时长或字节额度;
// 多线程下由最先拿到 2xx 的那条流启动窗口，其余线程共用同一份时长与额度。
type downloadWindow struct {
	duration           time.Duration
	cancel             context.CancelCauseFunc
	startOnce          sync.Once
	mu                 sync.Mutex
	startedAt          time.Time
	setupExpired       atomic.Bool
	measurementExpired atomic.Bool
	setupTimer         *time.Timer
	measurementTimer   *time.Timer
}

// newDownloadWindow 用 WithCancelCause 而不是 WithCancel:多线程路径要靠 context.Cause
// 区分「窗口正常到点」「额度用满」和「真的出错了」,只看 context.Canceled 是分不出来的。
func newDownloadWindow(ctx context.Context, dur time.Duration) (context.Context, *downloadWindow, error) {
	setupBudget := downloadSetupTime
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline) - dur
		if remaining <= 0 {
			return nil, nil, fmt.Errorf("测速任务剩余时间不足以完成 %s 吞吐测试", dur)
		}
		if remaining < setupBudget {
			setupBudget = remaining
		}
	}

	downloadCtx, cancel := context.WithCancelCause(ctx)
	window := &downloadWindow{duration: dur, cancel: cancel}
	window.setupTimer = time.AfterFunc(setupBudget, func() {
		window.mu.Lock()
		defer window.mu.Unlock()
		if window.startedAt.IsZero() {
			window.setupExpired.Store(true)
			cancel(errDownloadSetupExpired)
		}
	})
	return downloadCtx, window, nil
}

func (w *downloadWindow) startMeasurement() {
	w.startOnce.Do(func() {
		w.mu.Lock()
		defer w.mu.Unlock()
		if w.setupExpired.Load() {
			return
		}
		w.setupTimer.Stop()
		w.startedAt = time.Now()
		w.measurementTimer = time.AfterFunc(w.duration, func() {
			w.measurementExpired.Store(true)
			w.cancel(errDownloadWindowExpired)
		})
	})
}

// elapsed 取锁读 startedAt:多线程路径里 startMeasurement 由 worker 协程调用,
// 而 elapsed 由协调协程调用,不能靠“同一个协程”这个单线程才成立的前提。
func (w *downloadWindow) elapsed() time.Duration {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.startedAt.IsZero() {
		return 0
	}
	return time.Since(w.startedAt)
}

func (w *downloadWindow) stop() {
	if w.setupTimer != nil {
		w.setupTimer.Stop()
	}
	if w.measurementTimer != nil {
		w.measurementTimer.Stop()
	}
	w.cancel(context.Canceled)
}

// singleDownloadRetryDelay 给首个 2xx 前的两次重试留出递增的协议恢复时间。延迟仍受
// downloadCtx 控制并计入原有 setup 窗口，不能把一次测速悄悄延长成多个完整窗口。
func singleDownloadRetryDelay(attempt int) (time.Duration, bool) {
	switch attempt {
	case 1:
		return 250 * time.Millisecond, true
	case 2:
		return 750 * time.Millisecond, true
	default:
		return 0, false
	}
}

// waitSingleDownloadRetry 使用计时器而不是 time.Sleep，使父任务取消或 setup 超时
// 都能立即打断退避，不必等满 250/750 毫秒才收尾。
func waitSingleDownloadRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func downloadSingleTimed(ctx context.Context, dlURL string, dur time.Duration, maxBytes int64, bufSize, mixedPort int) (int64, time.Duration, error) {
	downloadCtx, window, err := newDownloadWindow(ctx, dur)
	if err != nil {
		return 0, 0, err
	}
	defer window.stop()

	var total int64
	attemptErrs := make([]error, 0, 3)
	measurementStarted := false
	for attempt := 1; attempt <= 3; attempt++ {
		remainingBytes := maxBytes
		if maxBytes > 0 {
			remainingBytes -= total
			if remainingBytes <= 0 {
				return total, window.elapsed(), nil
			}
		}

		n, _, gotFinal2xx, retryable, attemptErr := downloadSingleAttempt(
			downloadCtx, dlURL, remainingBytes, bufSize, mixedPort, nil, window.startMeasurement,
		)
		total += n
		if gotFinal2xx {
			measurementStarted = true
		}
		if attemptErr == nil {
			return total, window.elapsed(), nil
		}
		if ctx.Err() != nil {
			return total, window.elapsed(), ctx.Err()
		}
		// 必须是当前请求已经进入 2xx 响应体并实际读到数据，才可把本地计时器取消视为正常截止。
		// 这避免第二次请求卡在响应头前时借用首次断流留下的字节误报成功。
		if window.measurementExpired.Load() && gotFinal2xx && n > 0 && errors.Is(attemptErr, context.Canceled) {
			return total, window.elapsed(), nil
		}
		if window.measurementExpired.Load() {
			// 窗口在某次重试收到有效响应前已经结束时，不能再启动一个注定被取消的请求，
			// 也不能借用更早断流留下的字节把失败伪装成完整测速。
			return total, window.elapsed(), fmt.Errorf("下载测量窗口结束前未完成有效重试: %w", attemptErr)
		}
		if window.setupExpired.Load() {
			return total, window.elapsed(), fmt.Errorf("等待下载响应超时: %w", attemptErr)
		}
		if !retryable {
			return total, window.elapsed(), attemptErr
		}
		attemptErrs = append(attemptErrs, attemptErr)
		retryDelay, hasRetry := singleDownloadRetryDelay(attempt)
		if !hasRetry {
			return total, window.elapsed(), fmt.Errorf(
				"单线程下载重试仍失败: 第1次=%v；第2次=%v；第3次=%w",
				attemptErrs[0], attemptErrs[1], attemptErrs[2],
			)
		}
		if measurementStarted {
			// 首个 2xx 已启动吞吐窗口后，响应体断流仍可在剩余窗口和字节额度内重试，
			// 但不能再用启动退避消耗测量时间，否则窗口可能在纯等待中到期并误报失败。
			log.Printf(
				"[warn] 单线程下载第%d次尝试中断 bytes=%d: %v；正在剩余测量窗口内立即重试",
				attempt, n, attemptErr,
			)
			continue
		}
		log.Printf(
			"[warn] 单线程下载第%d次尝试中断 bytes=%d: %v；%s 后在剩余窗口内重试",
			attempt, n, attemptErr, retryDelay,
		)
		if waitErr := waitSingleDownloadRetry(downloadCtx, retryDelay); waitErr != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return total, window.elapsed(), ctxErr
			}
			if window.setupExpired.Load() {
				return total, window.elapsed(), fmt.Errorf("等待下载响应超时: %w", attemptErr)
			}
			return total, window.elapsed(), fmt.Errorf("单线程下载重试等待被取消: %w", waitErr)
		}
	}
	return total, window.elapsed(), attemptErrs[len(attemptErrs)-1]
}

// downloadSingleAttempt 只执行一次 HTTP 请求。gotFinal2xx 让调用方区分响应前失败与响应体中断；
// retryable 仅表示可在原有时间/字节窗口内重试，确定性配置错误不会被无意义重放。
func downloadSingleAttempt(ctx context.Context, dlURL string, maxBytes int64, bufSize, mixedPort int, quota *sharedDownloadQuota, onResponse func()) (int64, time.Duration, bool, bool, error) {
	client := proxyClientBuf(mixedPort, bufSize)
	defer client.CloseIdleConnections()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, dlURL, nil)
	if err != nil {
		// url.Error 会携带原始 URL；任务 URL 可能含临时签名，失败消息和容器日志都不回显它。
		return 0, 0, false, false, fmt.Errorf("构造下载请求失败")
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 mmwx-speedtest/1.0")
	req.Header.Set("Accept-Encoding", "identity")
	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return 0, time.Since(start), false, true, ctxErr
		}
		var urlErr *url.Error
		if errors.As(err, &urlErr) && urlErr.Err != nil {
			err = urlErr.Err // URL 可能带临时签名或令牌，错误与日志都不回显完整地址。
		}
		return 0, time.Since(start), false, true, fmt.Errorf("等待下载响应失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return 0, time.Since(start), false, false, fmt.Errorf("下载响应 HTTP %d", resp.StatusCode)
	}
	if onResponse != nil {
		onResponse()
	}
	var reader io.Reader = resp.Body
	if quota != nil {
		reader = &sharedQuotaReader{source: resp.Body, quota: quota}
	} else if maxBytes > 0 {
		reader = io.LimitReader(resp.Body, maxBytes)
	}
	buf := getCopyBuf(bufSize)
	defer putCopyBuf(buf)
	n, cerr := io.CopyBuffer(io.Discard, reader, *buf)
	elapsed := time.Since(start)
	if errors.Is(cerr, errDownloadQuotaReached) {
		return n, elapsed, true, false, nil
	}
	if cerr == nil {
		if n == 0 {
			// 其它流先用满共享额度时，本流可能刚收到 2xx 就拿不到 reserve；这是正常收尾，不是空响应。
			if quota != nil && errors.Is(context.Cause(ctx), errDownloadQuotaReached) {
				return 0, elapsed, true, false, nil
			}
			return 0, elapsed, true, true, fmt.Errorf("下载响应未包含数据")
		}
		return n, elapsed, true, false, nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return n, elapsed, true, true, ctxErr
	}
	return n, elapsed, true, true, cerr
}
