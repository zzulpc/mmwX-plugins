package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestDispatchProbeJob超出在途上限时丢弃(t *testing.T) {
	if got := len(probeJobSlots); got != 0 {
		t.Fatalf("用例开始前不应遗留 probe 任务槽，当前=%d", got)
	}
	for range probeMaxInflightJobs {
		probeJobSlots <- struct{}{}
	}
	defer func() {
		for range probeMaxInflightJobs {
			<-probeJobSlots
		}
	}()

	sent := false
	dispatchProbeJob(context.Background(), wsMsg{Type: "probe", JobID: "overflow", Targets: []string{"127.0.0.1:1"}}, func(wsMsg) error {
		sent = true
		return nil
	})

	// 伪造一条空 results 的 probe_result 会被主控读成「这批目标全部被墙」，
	// 所以在途超限时必须什么都不回，而不是回一条看起来正常的探测结果。
	if sent {
		t.Fatal("在途探测超限时不应回传任何 probe_result")
	}
}

func TestDispatchProbeJob正常执行并释放任务槽(t *testing.T) {
	assertProbeSlotsEmpty(t)
	done := make(chan wsMsg, 1)
	dispatchProbeJob(context.Background(), wsMsg{
		Type:    "probe",
		JobID:   "normal",
		Targets: []string{"格式非法"},
	}, func(msg wsMsg) error {
		done <- msg
		return nil
	})

	select {
	case msg := <-done:
		if msg.Type != "probe_result" || msg.JobID != "normal" || msg.Status != "ok" {
			t.Fatalf("正常 probe 回包不符: %#v", msg)
		}
		if len(msg.Results) != 1 || msg.Results[0].Error != "目标格式应为 host:port" {
			t.Fatalf("正常 probe 结果不符: %#v", msg.Results)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("正常 probe 未在限时内完成")
	}
	waitForSlotCount(t, probeJobSlots, 0, time.Second, "probe 任务槽")
	waitForSlotCount(t, probeDialSlots, 0, time.Second, "全局拨测槽")
}

func TestProbeJobBudget按批次计算并有硬上限(t *testing.T) {
	if got, want := probeJobBudget(wsMsg{}), probeJobGrace; got != want {
		t.Fatalf("空任务预算=%s，期望=%s", got, want)
	}
	if got, want := probeJobBudget(wsMsg{Targets: make([]string, 17), TimeoutMS: 1000}), 7*time.Second; got != want {
		t.Fatalf("17 个目标预算=%s，期望=%s", got, want)
	}
	if got, want := probeJobBudget(wsMsg{Targets: make([]string, 500), TimeoutMS: 60000}), 200*time.Second; got != want {
		t.Fatalf("超量目标与超长单拨超时的预算=%s，期望=%s", got, want)
	}
	maxInt := int(^uint(0) >> 1)
	if got := normalizedProbeTimeout(maxInt); got != probeMaxTimeout {
		t.Fatalf("极大毫秒整数不应在 Duration 乘法中溢出，实际超时=%s", got)
	}
}

func TestDispatchProbeJob整体截止时间取消等待并释放槽位(t *testing.T) {
	assertProbeSlotsEmpty(t)
	releaseDialSlots := occupyAllProbeDialSlots(t)

	var sent atomic.Bool
	started := time.Now()
	dispatchProbeJobWithBudget(context.Background(), wsMsg{
		Type:    "probe",
		JobID:   "deadline",
		Targets: []string{"127.0.0.1:1"},
	}, func(wsMsg) error {
		sent.Store(true)
		return nil
	}, 80*time.Millisecond)

	waitForSlotCount(t, probeJobSlots, 1, time.Second, "probe 任务槽")
	waitForSlotCount(t, probeJobSlots, 0, time.Second, "probe 任务槽")
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("整体截止后释放过慢: %s", elapsed)
	}
	if sent.Load() {
		t.Fatal("整体截止时不得回传含零值项的部分结果")
	}
	if got := len(probeDialSlots); got != probeConcurrency {
		t.Fatalf("取消等待不应吞掉其他任务持有的拨测槽，当前=%d", got)
	}
	releaseDialSlots()
}

func TestDispatchProbeJob父上下文取消时释放槽位(t *testing.T) {
	assertProbeSlotsEmpty(t)
	releaseDialSlots := occupyAllProbeDialSlots(t)
	ctx, cancel := context.WithCancel(context.Background())

	var sent atomic.Bool
	dispatchProbeJobWithBudget(ctx, wsMsg{
		Type:    "probe",
		JobID:   "cancel",
		Targets: []string{"127.0.0.1:1"},
	}, func(wsMsg) error {
		sent.Store(true)
		return nil
	}, time.Minute)
	waitForSlotCount(t, probeJobSlots, 1, time.Second, "probe 任务槽")
	cancel()
	waitForSlotCount(t, probeJobSlots, 0, time.Second, "probe 任务槽")
	if sent.Load() {
		t.Fatal("连接上下文取消时不得回传旧 probe 结果")
	}
	releaseDialSlots()
}

func TestRunProbe多任务共享进程级拨测上限(t *testing.T) {
	assertProbeSlotsEmpty(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseAll := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseAll()
	started := make(chan struct{}, 64)
	var active atomic.Int32
	var maximum atomic.Int32
	dial := func(ctx context.Context, target string, _ time.Duration) probeResult {
		current := active.Add(1)
		defer active.Add(-1)
		for {
			old := maximum.Load()
			if current <= old || maximum.CompareAndSwap(old, current) {
				break
			}
		}
		started <- struct{}{}
		select {
		case <-ctx.Done():
			return probeResult{Target: target, Error: ctx.Err().Error()}
		case <-release:
			return probeResult{Target: target, OK: true}
		}
	}

	results := make(chan wsMsg, 2)
	for jobIndex := range 2 {
		targets := make([]string, 32)
		for i := range targets {
			targets[i] = fmt.Sprintf("本地假目标-%d-%d", jobIndex, i)
		}
		job := wsMsg{Type: "probe", JobID: fmt.Sprintf("job-%d", jobIndex), Targets: targets}
		go runProbeWithDial(ctx, job, func(msg wsMsg) error {
			results <- msg
			return nil
		}, dial)
	}

	for range probeConcurrency {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("两个任务未能填满进程级拨测槽")
		}
	}
	select {
	case <-started:
		t.Fatal("多个 probe 任务合计超过了进程级拨测并发上限")
	case <-time.After(100 * time.Millisecond):
	}
	if got := active.Load(); got != probeConcurrency {
		t.Fatalf("活动拨测数=%d，期望=%d", got, probeConcurrency)
	}
	releaseAll()

	for range 2 {
		select {
		case msg := <-results:
			if len(msg.Results) != 32 {
				t.Fatalf("多任务完成结果数不符: %#v", msg)
			}
		case <-time.After(time.Second):
			t.Fatal("释放全局拨测槽后任务未完成")
		}
	}
	waitForSlotCount(t, probeDialSlots, 0, time.Second, "全局拨测槽")
	if got := maximum.Load(); got != probeConcurrency {
		t.Fatalf("观测到的最大拨测并发=%d，期望=%d", got, probeConcurrency)
	}
}

func TestConnectAndServe断线取消该连接派发的Probe(t *testing.T) {
	assertProbeSlotsEmpty(t)
	releaseDialSlots := occupyAllProbeDialSlots(t)

	probeSent := make(chan struct{})
	closeConnection := make(chan struct{})
	var closeOnce sync.Once
	closeServerConnection := func() { closeOnce.Do(func() { close(closeConnection) }) }
	handlerDone := make(chan struct{})
	handlerErr := make(chan error, 1)
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(handlerDone)
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			handlerErr <- fmt.Errorf("升级 WebSocket 失败: %w", err)
			return
		}
		defer conn.Close()
		var hello wsMsg
		if err := conn.ReadJSON(&hello); err != nil {
			handlerErr <- fmt.Errorf("读取 hello 失败: %w", err)
			return
		}
		if hello.Type != "hello" {
			handlerErr <- fmt.Errorf("首条消息不是 hello: %#v", hello)
			return
		}
		if err := conn.WriteJSON(wsMsg{
			Type:      "probe",
			JobID:     "connection-owned",
			Targets:   []string{"127.0.0.1:1"},
			TimeoutMS: 15000,
		}); err != nil {
			handlerErr <- fmt.Errorf("发送 probe 失败: %w", err)
			return
		}
		close(probeSent)
		<-closeConnection
	}))
	defer func() {
		// 失败路径也要先放行 handler 再关闭测试服务，否则 Close 会等待被本用例卡住的 handler。
		closeServerConnection()
		server.Close()
	}()

	serveDone := make(chan error, 1)
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	go func() {
		serveDone <- connectAndServeWithIPv6Check(wsURL, "local-test", nil, func() bool { return false })
	}()

	select {
	case <-probeSent:
	case err := <-handlerErr:
		t.Fatal(err)
	case <-time.After(2 * time.Second):
		t.Fatal("本地主控未派发 probe")
	}
	waitForSlotCount(t, probeJobSlots, 1, time.Second, "probe 任务槽")
	closeServerConnection()
	select {
	case err := <-serveDone:
		if err == nil {
			t.Fatal("服务端断开后 connectAndServe 应返回连接错误")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("服务端断开后 connectAndServe 未退出")
	}
	waitForSlotCount(t, probeJobSlots, 0, time.Second, "probe 任务槽")
	if got := len(probeDialSlots); got != probeConcurrency {
		t.Fatalf("断线取消不应吞掉预先占用的拨测槽，当前=%d", got)
	}
	releaseDialSlots()
	select {
	case <-handlerDone:
	case <-time.After(time.Second):
		t.Fatal("本地主控处理协程未退出")
	}
	select {
	case err := <-handlerErr:
		t.Fatal(err)
	default:
	}
}

func TestDialProbe(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("创建测试监听失败: %v", err)
	}
	defer listener.Close()
	openTarget := listener.Addr().String()

	closedPort, err := reserveLoopbackPort()
	if err != nil {
		t.Fatalf("分配测试端口失败: %v", err)
	}
	closedTarget := fmt.Sprintf("127.0.0.1:%d", closedPort)

	if result := dialProbe(context.Background(), openTarget, time.Second); !result.OK || result.Error != "" {
		t.Fatalf("可达目标应判定为通: %#v", result)
	}
	if result := dialProbe(context.Background(), closedTarget, time.Second); result.OK || result.Error == "" {
		t.Fatalf("已关闭端口应判定为不通并带原因: %#v", result)
	}

	// 格式非法时必须在拨号之前就拒绝:runProbe 的目标来自主控，
	// 不做校验就等于把任意字符串交给网络拨号器。
	for _, bad := range []string{"1.2.3.4", "no-port", "", "http://1.2.3.4:80"} {
		result := dialProbe(context.Background(), bad, time.Second)
		if result.OK || result.Error != "目标格式应为 host:port" {
			t.Fatalf("非法目标 %q 应被格式校验拦下: %#v", bad, result)
		}
	}

	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	started := time.Now()
	if result := dialProbe(canceledCtx, openTarget, time.Second); result.OK || result.Error == "" {
		t.Fatalf("已取消上下文不应继续拨号: %#v", result)
	}
	if elapsed := time.Since(started); elapsed > 100*time.Millisecond {
		t.Fatalf("已取消拨号返回过慢: %s", elapsed)
	}
}

func assertProbeSlotsEmpty(t *testing.T) {
	t.Helper()
	if jobs, dials := len(probeJobSlots), len(probeDialSlots); jobs != 0 || dials != 0 {
		t.Fatalf("用例开始前探测槽位不干净: jobs=%d dials=%d", jobs, dials)
	}
}

func occupyAllProbeDialSlots(t *testing.T) func() {
	t.Helper()
	if got := len(probeDialSlots); got != 0 {
		t.Fatalf("占用前全局拨测槽应为空，当前=%d", got)
	}
	for range probeConcurrency {
		probeDialSlots <- struct{}{}
	}
	var once sync.Once
	release := func() {
		once.Do(func() {
			for range probeConcurrency {
				<-probeDialSlots
			}
		})
	}
	t.Cleanup(release)
	return release
}

func waitForSlotCount(t *testing.T, slots chan struct{}, want int, timeout time.Duration, label string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if len(slots) == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("%s 数量=%d，期望=%d", label, len(slots), want)
}
