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

// runMu 串行化测速:一次只跑一个节点,避免并发抢带宽导致结果失真。
var runMu sync.Mutex

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

// 测速 buffer / 线程的取值边界。峰值内存 ≈ BufSize × Threads,maxSpeedTotalMem 防家用测速端 OOM。
const (
	defaultBufSize    = 1 << 20          // 1MB(= 历史固定值,向后兼容)
	minBufSize        = 64 << 10         // 64KB
	maxBufSize        = 16 << 20         // 16MB
	maxSpeedThreads   = 64               // 并发下载线程上限
	maxSpeedTotalMem  = 256 << 20        // BufSize×Threads 上限:超了缩 BufSize
	downloadSetupTime = 10 * time.Second // 单线程首个 2xx 的独立准备预算，不挤占 8 秒吞吐窗口
)

var (
	errDownloadWindowExpired = errors.New("下载测速窗口已结束")
	errDownloadQuotaReached  = errors.New("下载字节额度已用尽")
)

// clampSpeedTestParams 归一 bufSize(字节)与 threads,并把 bufSize×threads 峰值内存收敛到 maxSpeedTotalMem 内。
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
	if int64(bufSize)*int64(threads) > maxSpeedTotalMem {
		bufSize = maxSpeedTotalMem / threads
		if bufSize < minBufSize {
			bufSize = minBufSize
		}
	}
	return bufSize, threads
}

// RunNodeTest 用已选择的代理内核启动单节点代理，并测量延迟、出口 IP 与下行吞吐。
func RunNodeTest(ctx context.Context, runtimeInfo proxyRuntime, clashConfigJSON string, opts Options) (Result, error) {
	if opts.TestDuration <= 0 {
		opts.TestDuration = defaultTestDuration
	}
	if opts.Timeout <= 0 {
		opts.Timeout = opts.TestDuration + 30*time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()

	runMu.Lock()
	defer runMu.Unlock()
	if err := ctx.Err(); err != nil {
		return Result{}, fmt.Errorf("测速任务等待执行超时: %w", err)
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
	name, _ := proxy["name"].(string)
	if name == "" {
		name = "node"
		proxy["name"] = name
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
		config, err = buildMihomoConfig(proxy, name, mixedPort)
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
		return Result{LatencyMs: latency, EgressIP: egressIP}, nil
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

// buildMihomoConfig 保留原有单节点 Clash 配置路径，仅把本地端口改为每任务动态分配。
func buildMihomoConfig(proxy map[string]any, name string, mixedPort int) ([]byte, error) {
	normalizedProxy, ok := normalizeJSONNumbers(proxy).(map[string]any)
	if !ok {
		return nil, fmt.Errorf("规范化 Mihomo 节点配置失败")
	}
	mini := map[string]any{
		"mixed-port":          mixedPort,
		"allow-lan":           false,
		"mode":                "rule",
		"log-level":           "warning",
		"external-controller": "127.0.0.1:0",
		"proxies":             []map[string]any{normalizedProxy},
		"proxy-groups": []map[string]any{
			{"name": "PROXY", "type": "select", "proxies": []string{name}},
		},
		"rules": []string{"MATCH,PROXY"},
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
	client.Timeout = 8 * time.Second
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

// proxyClient 经本次任务的 mixed 入站走代理。
// 单流测速调优:1MB ReadBufferSize / 禁 HTTP/2(单流被流控限速)/ 禁 Compression / 复用 Transport
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
func measureLatency(ctx context.Context, mixedPort int) int64 {
	client := proxyClient(mixedPort)
	defer client.CloseIdleConnections()
	client.Timeout = 10 * time.Second
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, latencyProbeURL, nil)
	if err != nil {
		return -1
	}
	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return -1
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return time.Since(start).Milliseconds()
}

// measureLatencyCloudflare 用 Cloudflare 204 多次采样,取最快 2 个均值;
// 首包受 TLS 握手 / mihomo cold-start 影响,平均后更接近"真连接延迟"。全部失败返回 -1。
func measureLatencyCloudflare(ctx context.Context, mixedPort, samples int) int64 {
	if samples <= 0 {
		samples = cfLatencySamples
	}
	client := proxyClient(mixedPort)
	defer client.CloseIdleConnections()
	client.Timeout = 8 * time.Second
	probes := make([]int64, 0, samples)
	for i := 0; i < samples; i++ {
		if ctx.Err() != nil {
			break
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, cfLatencyProbeURL, nil)
		if err != nil {
			continue
		}
		start := time.Now()
		resp, err := client.Do(req)
		if err != nil {
			continue
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		probes = append(probes, time.Since(start).Milliseconds())
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

	timedCtx, stopTimer := context.WithTimeoutCause(ctx, dur, errDownloadWindowExpired)
	defer stopTimer()
	dlCtx, cancel := context.WithCancelCause(timedCtx)
	defer cancel(context.Canceled)

	var wg sync.WaitGroup
	results := make([]int64, threads)
	errs := make([]error, threads)
	var quota *sharedDownloadQuota
	if maxBytes > 0 {
		quota = &sharedDownloadQuota{maxBytes: maxBytes, cancel: cancel}
	}
	start := time.Now()
	for i := range threads {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			n, _, _, _, e := downloadSingleAttempt(dlCtx, dlURL, maxBytes, bufSize, mixedPort, quota, nil)
			results[idx] = n
			errs[idx] = e
		}(i)
	}
	wg.Wait()
	elapsed := time.Since(start)
	if err := ctx.Err(); err != nil {
		return 0, elapsed, err
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

// singleDownloadWindow 把“等待首个有效响应”和“吞吐计时”分开。
// 同一个窗口最多容纳三次串行请求，断流重试不会额外获得一份测速时长或字节额度。
type singleDownloadWindow struct {
	duration           time.Duration
	cancel             context.CancelFunc
	startOnce          sync.Once
	mu                 sync.Mutex
	startedAt          time.Time
	setupExpired       atomic.Bool
	measurementExpired atomic.Bool
	setupTimer         *time.Timer
	measurementTimer   *time.Timer
}

func newSingleDownloadWindow(ctx context.Context, dur time.Duration) (context.Context, *singleDownloadWindow, error) {
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

	downloadCtx, cancel := context.WithCancel(ctx)
	window := &singleDownloadWindow{duration: dur, cancel: cancel}
	window.setupTimer = time.AfterFunc(setupBudget, func() {
		window.mu.Lock()
		defer window.mu.Unlock()
		if window.startedAt.IsZero() {
			window.setupExpired.Store(true)
			cancel()
		}
	})
	return downloadCtx, window, nil
}

func (w *singleDownloadWindow) startMeasurement() {
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
			w.cancel()
		})
	})
}

func (w *singleDownloadWindow) elapsed() time.Duration {
	if w.startedAt.IsZero() {
		return 0
	}
	return time.Since(w.startedAt)
}

func (w *singleDownloadWindow) stop() {
	if w.setupTimer != nil {
		w.setupTimer.Stop()
	}
	if w.measurementTimer != nil {
		w.measurementTimer.Stop()
	}
	w.cancel()
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
	downloadCtx, window, err := newSingleDownloadWindow(ctx, dur)
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
