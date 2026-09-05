// mmwx-speedtester:妙妙屋X 家用测速端(PRO speed_test Phase 2)。
// 部署在用户家里的服务器/电脑上,主动反向连接主控(解决家庭无公网 IP);
// 收到测速任务后用 Mihomo 或 sing-box 内核对指定节点下载测速,结果经同一连接回传。
package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

//go:embed VERSION
var versionRaw string

// version 随 hello 上报给主控,用于在测速端列表里展示。
//
// 直接 embed VERSION 文件而不是写死常量:release.sh 会自动 bump 那个文件,
// 手写常量必然和它漂移。注意主控判定「能否承担可达性探测」看的是 caps 而非版本号,
// 版本只用于展示。
var version = strings.TrimSpace(versionRaw)

const (
	// 主控 v0.4.9 会并发派发批量任务，因此容纳少量在途任务；真正测速仍由 runMu 一次执行一个，
	// 超出四个才回报忙，既兼容常见小批量，也避免无界等待。
	runConcurrency = 4
	// 单次 probe 最多拨测多少个目标 —— 防止主控(或被攻破的主控)拿家用测速端当端口扫描器。
	probeMaxTargets = 200
	// 并发拨测数上限:家用带宽/路由器连接表有限,开太大反而互相拖慢并可能触发 NAT 表爆掉。
	// 这是【进程级】上限而不是每个 probe 任务各自一份 —— 按任务分配的话,主控连发 N 条 probe
	// 就能把并发拨测放大到 16N、目标数放大到 200N,上面那条「不当端口扫描器」的约束等于没有。
	probeConcurrency = 16
	// 在途 probe 任务上限。正常节奏是几十个节点每 5 分钟一轮,一次一两个任务,8 个绰绰有余;
	// 超出只记日志丢弃,不像 run 那样回一条 failed —— probe_result 的 results 为空时,
	// 主控很可能读成「这批目标全部被墙」,比不回包更糟,而 probe_result{status:"failed"}
	// 的主控侧兼容性尚未确认,这里不擅自改协议。
	probeMaxInflightJobs = 8
	probeMinTimeout      = 500 * time.Millisecond
	probeMaxTimeout      = 15 * time.Second
	// 整体截止时间按「独占 16 路拨测时所需批次」计算，再留少量调度余量。
	// 即使多个任务共享拨测槽，单个任务也不会因排队无限拖长；200 个目标、15 秒单拨时最多约 200 秒。
	probeJobGrace = 5 * time.Second
	// 单轮最多逐条打印多少个不可达目标。整批全挂(断网、上游拒绝)时不该刷几百行。
	probeLogFailLimit = 20
)

var (
	runJobSlots = make(chan struct{}, runConcurrency)
	// probeJobSlots 限在途 probe 任务数,probeDialSlots 限全进程同时在拨的 TCP 连接数。
	// 两个都必须有:只限任务数挡不住单任务 200 个目标,只限拨测数挡不住 goroutine 无限增长。
	probeJobSlots  = make(chan struct{}, probeMaxInflightJobs)
	probeDialSlots = make(chan struct{}, probeConcurrency)
)

type wsMsg struct {
	Type        string  `json:"type"`
	JobID       string  `json:"job_id,omitempty"`
	ClashConfig string  `json:"clash_config,omitempty"`
	Bytes       int64   `json:"bytes,omitempty"`
	URL         string  `json:"url,omitempty"`
	Threads     int     `json:"threads,omitempty"`
	BufSize     int64   `json:"buf_size,omitempty"`     // 每次收发 buffer 字节数(默认 1MB)
	LatencyOnly bool    `json:"latency_only,omitempty"` // true 仅测真连接延迟(Cloudflare 204)
	DownMbps    float64 `json:"down_mbps,omitempty"`
	LatencyMs   int64   `json:"latency_ms,omitempty"`
	EgressIP    string  `json:"egress_ip,omitempty"`
	Status      string  `json:"status,omitempty"`
	Error       string  `json:"error,omitempty"`
	Name        string  `json:"name,omitempty"`

	// ---- 可达性探测(被墙判定)。与测速无关:纯 TCP 拨测目标 host:port,**不经 mihomo** ——
	// 要判的是"这个地址从本机所在网络能不能连上",套代理就失去意义了。
	Version   string        `json:"version,omitempty"` // hello 携带,主控据此展示
	Caps      []string      `json:"caps,omitempty"`    // hello 携带的能力集,老版本没有此字段
	Targets   []string      `json:"targets,omitempty"` // master→tester:待拨测的 host:port 列表
	TimeoutMS int           `json:"timeout_ms,omitempty"`
	Results   []probeResult `json:"results,omitempty"` // tester→master
}

// probeResult 单个目标的拨测结果。
type probeResult struct {
	Target    string `json:"target"`
	OK        bool   `json:"ok"`
	LatencyMs int64  `json:"latency_ms,omitempty"`
	Error     string `json:"error,omitempty"`
}

func main() {
	master := flag.String("master", envOr("MMWX_MASTER", ""), "主控地址,如 https://x.miaomiaowu.net")
	token := flag.String("token", envOr("MMWX_SPEEDTEST_TOKEN", ""), "测速端配对令牌(主控插件页生成)")
	name := flag.String("name", envOr("MMWX_SPEEDTEST_NAME", "home-tester"), "测速端名称(展示用)")
	dataDir := flag.String("data-dir", envOr("MMWX_DATA_DIR", ""), "数据目录（内核缓存位于 bin 子目录；默认在可执行文件旁）")
	flag.Parse()

	if *master == "" || *token == "" {
		log.Fatal("必须提供 -master 和 -token(或环境变量 MMWX_MASTER / MMWX_SPEEDTEST_TOKEN)")
	}
	if err := configureDataDir(*dataDir); err != nil {
		log.Fatalf("配置数据目录失败: %v", err)
	}
	wsURL, err := buildWSURL(*master, *token)
	if err != nil {
		log.Fatalf("解析 master 地址失败: %v", err)
	}

	// 预热:确保 mihomo 可用(没有则自动下载)。
	if _, err := EnsureMihomo(context.Background()); err != nil {
		log.Printf("[warn] mihomo 预热失败(测速时会重试): %v", err)
	}
	// sing-box 仅服务 Snell v6，双内核镜像内已固定版本；本地运行时可用 SING_BOX_BIN 指定。
	if _, err := EnsureSingBox(context.Background()); err != nil {
		log.Printf("[warn] sing-box 预热失败(Snell v6 测速时会重试): %v", err)
	}

	log.Printf("[speedtester] %s 启动,主控=%s", *name, *master)
	log.Printf("[speedtester] 拨号目标 %s", maskedURL(wsURL))

	// 指数退避重连:1s → 2s → 4s ... 封顶 60s。connectAndServe 内每次成功握手后会通过
	// resetBackoff 函数把它重置回 1s — 防止"一次断网长时间后,网恢复了仍要等 60s 才重连"。
	backoff := time.Second
	const maxBackoff = 60 * time.Second
	resetBackoff := func() { backoff = time.Second }
	for {
		err := connectAndServe(wsURL, *name, resetBackoff)
		if err != nil {
			log.Printf("[speedtester] 连接断开: %v;%v 后重连", err, backoff)
		} else {
			// 正常 return 大概率不会发生(内部 for-loop 只在 read error 时 return),
			// 真发生也按短间隔重连
			log.Printf("[speedtester] 连接结束;%v 后重连", backoff)
		}
		time.Sleep(backoff)
		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

// maskedURL 隐藏 query 里的 token,避免日志泄露配对令牌。
func maskedURL(s string) string {
	u, err := url.Parse(s)
	if err != nil {
		return s
	}
	q := u.Query()
	if tok := q.Get("token"); tok != "" {
		if len(tok) > 8 {
			q.Set("token", tok[:4]+"…"+tok[len(tok)-4:])
		} else {
			q.Set("token", "***")
		}
		u.RawQuery = q.Encode()
	}
	return u.String()
}

// 心跳节奏 + 读超时设计:
//   - 客户端每 30s 发一次应用层 ping(wsMsg{type:"ping"}),主控应回 pong
//   - 同时挂 WebSocket 协议层 PongHandler,主控也可主动 ping 我们 → 我们回 pong
//   - SetReadDeadline 设为 75s(2.5 × 30s 心跳间隔,容忍 1 次 pong 丢)
//   - 收到任何消息(text / pong)都刷新 read deadline → 真没消息才会触发超时
//
// 这样既能检测主动死亡(对端崩 / NAT keepalive 失效 / 网线被拔),又不会因为偶发卡顿就误判断开。
const (
	heartbeatInterval = 30 * time.Second
	readDeadline      = 75 * time.Second
)

func connectAndServe(wsURL, name string, onConnected func()) error {
	return connectAndServeWithIPv6Check(wsURL, name, onConnected, hasIPv6)
}

// connectAndServeWithIPv6Check 把能力检测作为参数，便于用纯本地 WebSocket 验证连接生命周期，
// 避免测试为了构造 probe6 能力而访问公网。
func connectAndServeWithIPv6Check(wsURL, name string, onConnected func(), ipv6Check func() bool) error {
	log.Printf("[speedtester] 正在拨号主控 WebSocket(15s 超时)...")
	// DefaultDialer 没有 HandshakeTimeout,DNS / TCP 阻塞时会一直挂没反馈;
	// 这里显式 15s 超时 + 失败时把 HTTP 状态码也打出来,便于区分:
	//   - "no such host" → 主控 URL 域名错或 DNS 不通
	//   - "connection refused" → 主控不在该地址监听
	//   - "HTTP 401" → token 错或已过期
	//   - "HTTP 404" → 主控版本太老,没有 /api/speedtest/tester/ws 端点
	dialer := &websocket.Dialer{
		HandshakeTimeout: 15 * time.Second,
		Proxy:            http.ProxyFromEnvironment,
	}
	conn, resp, err := dialer.Dial(wsURL, nil)
	if err != nil {
		extra := ""
		if resp != nil {
			extra = fmt.Sprintf(" (HTTP %d %s)", resp.StatusCode, http.StatusText(resp.StatusCode))
		}
		log.Printf("[speedtester] ✗ 拨号失败: %v%s", err, extra)
		return err
	}
	defer conn.Close()
	connectionCtx, cancelConnection := context.WithCancel(context.Background())
	defer cancelConnection()
	log.Printf("[speedtester] ✓ 已连接主控,发送 hello")
	if onConnected != nil {
		onConnected() // 重置 backoff,下次断开从 1s 重新开始
	}

	// 初始读超时 — 服务端必须在 readDeadline 内有任何消息(包括 pong),否则强制断
	_ = conn.SetReadDeadline(time.Now().Add(readDeadline))
	// 收到协议层 pong 也算"活着" — 把 deadline 续上
	conn.SetPongHandler(func(string) error {
		_ = conn.SetReadDeadline(time.Now().Add(readDeadline))
		return nil
	})

	var writeMu = make(chan struct{}, 1)
	writeMu <- struct{}{}
	send := func(m wsMsg) error {
		// 写锁也必须受连接生命周期约束，否则旧连接上的结果可能卡在另一条写操作后，
		// 等新连接建立后才尝试回传，既占着任务槽也制造误导日志。
		select {
		case <-connectionCtx.Done():
			return connectionCtx.Err()
		case <-writeMu:
		}
		defer func() { writeMu <- struct{}{} }()
		select {
		case <-connectionCtx.Done():
			return connectionCtx.Err()
		default:
		}
		_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
		data, _ := json.Marshal(m)
		if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
			// 写失败已经能证明这条连接不可用，立即取消它派发的任务，
			// 不必再等读超时到期才释放测速与拨测资源。
			cancelConnection()
			return err
		}
		return nil
	}

	// hello 带上版本与能力集。老版本只发 Name —— 主控据「有没有 caps」判断能否派可达性探测,
	// 否则给老测速端派 probe 会被静默丢弃,主控只能干等超时。
	// probe6:本机能拨通公网 IPv6 才声明 —— 否则主控会把 v6 节点(都是外网 v6 地址)误报被墙
	//(此测速端对所有 v6 目标都 network unreachable)。有 probe6 的源才被主控派去探 v6 节点。
	caps := []string{"speedtest", "probe"}
	if ipv6Check != nil && ipv6Check() {
		caps = append(caps, "probe6")
	}
	if err := send(wsMsg{Type: "hello", Name: name, Version: version, Caps: caps}); err != nil {
		return fmt.Errorf("发送 hello 失败: %w", err)
	}

	// 心跳保活 — 应用层 ping(主控收到回 pong 一样会续 deadline)
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		t := time.NewTicker(heartbeatInterval)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				if send(wsMsg{Type: "ping"}) != nil {
					return
				}
			}
		}
	}()

	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			// 先取消再返回，确保这条连接派发的任务不会在函数退出边缘继续占槽或回传旧结果。
			cancelConnection()
			return err
		}
		// 收到任何 text 帧都算活着,续 deadline
		_ = conn.SetReadDeadline(time.Now().Add(readDeadline))
		var msg wsMsg
		if json.Unmarshal(data, &msg) != nil {
			continue
		}
		switch msg.Type {
		case "run":
			dispatchRunJob(connectionCtx, msg, send)
		case "probe":
			dispatchProbeJob(connectionCtx, msg, send)
		}
		// pong 等忽略
	}
}

// dispatchRunJob 从收到消息时就启动超时，并继承连接取消；断线后的结果无法回传，
// 不应让旧任务继续占用带宽。非阻塞信号量仍拒绝超出容量的任务。
func dispatchRunJob(parentCtx context.Context, job wsMsg, send func(wsMsg) error) {
	ctx, cancel := context.WithTimeout(parentCtx, defaultTestDuration+30*time.Second)
	if ctx.Err() != nil {
		cancel()
		return
	}
	select {
	case runJobSlots <- struct{}{}:
		go func() {
			defer cancel()
			defer func() { <-runJobSlots }()
			runJob(ctx, job, send)
		}()
	default:
		cancel()
		log.Printf("[speedtester] 拒绝测速任务 job=%s: 测速端忙", job.JobID)
		if err := send(wsMsg{Type: "result", JobID: job.JobID, Status: "failed", Error: "测速端忙"}); err != nil {
			log.Printf("[speedtester] 回传忙状态失败: %v", err)
		}
	}
}

// dispatchProbeJob 用非阻塞信号量限制在途探测数。超出上限时只记日志丢弃:
// 见 probeMaxInflightJobs 的说明 —— 伪造一条空 results 的回包会被主控读成「全部被墙」。
func dispatchProbeJob(parentCtx context.Context, job wsMsg, send func(wsMsg) error) {
	dispatchProbeJobWithBudget(parentCtx, job, send, probeJobBudget(job))
}

// dispatchProbeJobWithBudget 从收到任务时就启动整体计时。独立入口让测试可以缩短预算，
// 在不等待生产级数十秒超时的情况下验证取消和槽位释放。
func dispatchProbeJobWithBudget(parentCtx context.Context, job wsMsg, send func(wsMsg) error, budget time.Duration) {
	ctx, cancel := context.WithTimeout(parentCtx, budget)
	if ctx.Err() != nil {
		cancel()
		return
	}
	select {
	case probeJobSlots <- struct{}{}:
		go func() {
			defer cancel()
			defer func() { <-probeJobSlots }()
			runProbe(ctx, job, send)
		}()
	default:
		cancel()
		log.Printf("[speedtester] 丢弃可达性探测 job=%s: 在途探测已达上限 %d", job.JobID, probeMaxInflightJobs)
	}
}

func runJob(ctx context.Context, job wsMsg, send func(wsMsg) error) {
	effectiveBuf, effectiveThreads := clampSpeedTestParams(int(job.BufSize), job.Threads)
	log.Printf(
		"[speedtester] 收到测速任务 job=%s threads=%d buf=%d bytes=%d latency_only=%t",
		job.JobID, effectiveThreads, effectiveBuf, job.Bytes, job.LatencyOnly,
	)
	runtimeInfo, err := resolveProxyRuntime(ctx, job.ClashConfig)
	if err != nil {
		_ = send(wsMsg{Type: "result", JobID: job.JobID, Status: "failed", Error: err.Error()})
		return
	}
	log.Printf("[speedtester] job=%s 使用内核=%s", job.JobID, runtimeInfo.Core)
	res, terr := RunNodeTest(ctx, runtimeInfo, job.ClashConfig, Options{
		TestBytes:   job.Bytes,
		TestURL:     job.URL,
		Threads:     job.Threads,
		BufSize:     int(job.BufSize),
		LatencyOnly: job.LatencyOnly,
	})
	out := wsMsg{Type: "result", JobID: job.JobID, LatencyMs: res.LatencyMs, EgressIP: res.EgressIP}
	if terr != nil {
		out.Status = "failed"
		out.Error = terr.Error()
		log.Printf("[speedtester] job=%s 测速失败: %v", job.JobID, terr)
	} else {
		out.Status = "ok"
		out.DownMbps = res.DownMbps
	}
	if err := send(out); err != nil {
		log.Printf("[speedtester] 回传结果失败: %v", err)
		return
	}
	log.Printf("[speedtester] job=%s 完成 status=%s down=%.1fMbps", job.JobID, out.Status, out.DownMbps)
}

// buildWSURL 把 http(s) 主控地址转成 ws(s) 的测速端连接 URL。
func buildWSURL(master, token string) (string, error) {
	u, err := url.Parse(strings.TrimRight(master, "/"))
	if err != nil {
		return "", err
	}
	// url.Parse 对少写协议头的地址不报错,只是解析成完全不同的东西:
	//   "x.example.net"    → 整段落进 Path,Host 为空,最终拨 "wss:///api/..."
	//   "example.net:8080" → example.net 当 scheme、8080 当 Opaque,
	//                        下面的 u.Path 赋值会被 Opaque 静默吃掉,最终拨 "wss:8080"
	// 两种都只能在拨号阶段报一句看不出根因的错(no such host / 域名不合法),
	// 而根因只是 -master 少写了 https://,所以在这里就挡住并直说。
	if u.Opaque != "" || u.Host == "" {
		return "", fmt.Errorf("主控地址必须使用 http://、https://、ws:// 或 wss:// 前缀,当前为 %q", master)
	}
	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	case "http":
		u.Scheme = "ws"
	case "wss", "ws":
		// 已是 ws
	default:
		return "", fmt.Errorf("主控地址必须使用 http://、https://、ws:// 或 wss:// 前缀,当前为 %q", master)
	}
	u.Path = "/api/speedtest/tester/ws"
	q := u.Query()
	q.Set("token", token)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// runProbe 执行一次可达性探测:并发 TCP 拨测每个目标,回报是否连得上 + 握手耗时。
//
// 为什么是裸 TCP 而不是走 mihomo:这里判的是「该 host:port 从本机所在网络能否建立连接」,
// 也就是被墙与否。套上代理就变成了测代理链路,结论完全不同。
//
// 只报「连得上/连不上」,不做任何内容读写 —— 它不是端口扫描器,也不该被当成一个。
// hasIPv6 检测本机能否拨通【公网 IPv6】,决定是否向主控声明 probe6 能力。
// 家用测速端多在国内家庭网络,IPv6 可能没有 / 只通内网;而 mmwX 的 v6 节点都是外网 v6 地址,
// 必须验证能连通公网 v6 才有资格探 v6 节点 —— 否则声明 probe6 反会把 v6 节点误报被墙
// (本机对所有外网 v6 目标都 network unreachable)。逐个试几个全球可达的公网 v6 端点,任一拨通即有。
func hasIPv6() bool {
	endpoints := []string{
		"[2606:4700:4700::1111]:443", // Cloudflare DNS
		"[2001:4860:4860::8888]:443", // Google DNS
	}
	for _, ep := range endpoints {
		if c, err := net.DialTimeout("tcp", ep, 3*time.Second); err == nil {
			_ = c.Close()
			return true
		}
	}
	return false
}

// normalizedProbeTimeout 统一任务预算与实际拨号使用的单目标超时，避免二者口径漂移。
func normalizedProbeTimeout(timeoutMS int) time.Duration {
	// 先按毫秒整数比较再转 Duration，避免恶意极大整数在乘法时溢出成负值或短超时。
	if timeoutMS < int(probeMinTimeout/time.Millisecond) {
		return probeMinTimeout
	}
	if timeoutMS > int(probeMaxTimeout/time.Millisecond) {
		return probeMaxTimeout
	}
	return time.Duration(timeoutMS) * time.Millisecond
}

// probeJobBudget 按截断后的目标数计算独占全局拨测槽时所需批次，并追加固定调度余量。
// 多任务竞争导致等待超过这份预算时宁可取消且不回部分结果，也不让旧巡检拖到下一轮。
func probeJobBudget(job wsMsg) time.Duration {
	targetCount := len(job.Targets)
	if targetCount > probeMaxTargets {
		targetCount = probeMaxTargets
	}
	batches := (targetCount + probeConcurrency - 1) / probeConcurrency
	return time.Duration(batches)*normalizedProbeTimeout(job.TimeoutMS) + probeJobGrace
}

func runProbe(ctx context.Context, job wsMsg, send func(wsMsg) error) {
	runProbeWithDial(ctx, job, send, dialProbe)
}

// runProbeWithDial 每个任务最多启动 16 个 worker 轮流领取目标。不能为 200 个目标预建
// 200 个阻塞协程，否则先到任务会在全局信号量前堆满等待者，后到任务很难公平取得拨测机会。
func runProbeWithDial(
	ctx context.Context,
	job wsMsg,
	send func(wsMsg) error,
	dial func(context.Context, string, time.Duration) probeResult,
) {
	targets := job.Targets
	if len(targets) > probeMaxTargets {
		log.Printf("[speedtester] probe 目标数 %d 超过上限 %d,已截断", len(targets), probeMaxTargets)
		targets = targets[:probeMaxTargets]
	}
	timeout := normalizedProbeTimeout(job.TimeoutMS)
	log.Printf("[speedtester] 收到可达性探测 job=%s targets=%d timeout=%s", job.JobID, len(targets), timeout)
	started := time.Now()

	results := make([]probeResult, len(targets))
	targetIndexes := make(chan int, len(targets))
	for i := range targets {
		targetIndexes <- i
	}
	close(targetIndexes)

	workerCount := len(targets)
	if workerCount > probeConcurrency {
		workerCount = probeConcurrency
	}
	var wg sync.WaitGroup
	for range workerCount {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range targetIndexes {
				if ctx.Err() != nil {
					return
				}
				select {
				case <-ctx.Done():
					return
				case probeDialSlots <- struct{}{}:
				}
				results[i] = dial(ctx, targets[i], timeout)
				<-probeDialSlots
			}
		}()
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		// 取消或整体超时都不能回传尚未填满的 results；主控会把零值项误判成目标不可达。
		log.Printf("[speedtester] 探测取消 job=%s: %v", job.JobID, err)
		return
	}

	// 逐条打印不可达的目标。主控只拿到 ok/不ok,判定被墙的依据到底是"连接超时"还是
	// "域名解析不了",只有这里看得见 —— 排查误判时没这行日志基本无从下手。
	// 可达的不逐条打(几十个节点每 5 分钟一轮会刷屏),汇总行给数量和耗时就够。
	okN, failed := 0, 0
	for _, r := range results {
		if r.OK {
			okN++
			continue
		}
		failed++
		if failed <= probeLogFailLimit {
			log.Printf("[speedtester]   ✗ %s: %s", r.Target, r.Error)
		}
	}
	if failed > probeLogFailLimit {
		// 整批全挂时(断网、上游拒绝)别把日志刷成几百行
		log.Printf("[speedtester]   ✗ 另有 %d 个目标不可达(已省略)", failed-probeLogFailLimit)
	}
	log.Printf("[speedtester] 探测完成 job=%s 可达 %d/%d 耗时 %s",
		job.JobID, okN, len(results), time.Since(started).Round(time.Millisecond))
	if err := send(wsMsg{Type: "probe_result", JobID: job.JobID, Status: "ok", Results: results}); err != nil {
		log.Printf("[speedtester] 回传探测结果失败 job=%s: %v", job.JobID, err)
	}
}

// dialProbe 拨一个 host:port,返回是否可达与握手耗时。
func dialProbe(ctx context.Context, target string, timeout time.Duration) probeResult {
	res := probeResult{Target: target}
	if _, _, err := net.SplitHostPort(target); err != nil {
		res.Error = "目标格式应为 host:port"
		return res
	}
	start := time.Now()
	dialer := net.Dialer{Timeout: timeout}
	conn, err := dialer.DialContext(ctx, "tcp", target)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	_ = conn.Close()
	res.OK = true
	res.LatencyMs = time.Since(start).Milliseconds()
	return res
}
