package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"gopkg.in/yaml.v3"
)

const lifecycleTestProxy = `{"name":"测试节点","type":"ss","server":"127.0.0.1","port":1,"cipher":"aes-128-gcm","password":"test-password"}`

// newLifecycleTestCore 复用测试二进制模拟内核，避免依赖 Python、真实代理内核或公网服务。
func newLifecycleTestCore(t *testing.T, handler http.HandlerFunc) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("该进程生命周期测试使用 POSIX 启动脚本")
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	t.Setenv("MMWX_TEST_CORE_CONTROL", server.URL)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	core := filepath.Join(t.TempDir(), "fake-mihomo")
	quotedExecutable := "'" + strings.ReplaceAll(executable, "'", "'\"'\"'") + "'"
	script := "#!/bin/sh\nexec " + quotedExecutable + " -test.run '^TestLifecycleFakeCoreProcess$' -- \"$@\"\n"
	if err := os.WriteFile(core, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MIHOMO_BIN", core)
	return core
}

// TestLifecycleFakeCoreProcess 只在父测试启动的子进程中运行；所有 CONNECT 都交给回环控制端处理。
func TestLifecycleFakeCoreProcess(t *testing.T) {
	controlURL := os.Getenv("MMWX_TEST_CORE_CONTROL")
	if controlURL == "" {
		return
	}
	if !strings.HasPrefix(controlURL, "http://127.0.0.1:") {
		t.Fatal("假内核控制端必须为回环地址")
	}
	configPath := ""
	for index, arg := range os.Args {
		if arg == "-v" {
			fmt.Println("Mihomo Meta v1.19.30")
			os.Exit(0)
		}
		if arg == "-f" && index+1 < len(os.Args) {
			configPath = os.Args[index+1]
		}
	}
	config, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		MixedPort int `yaml:"mixed-port"`
	}
	if err := yaml.Unmarshal(config, &decoded); err != nil {
		t.Fatal(err)
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		response, err := http.Get(controlURL)
		if err != nil {
			http.Error(w, "本地控制端不可用", http.StatusBadGateway)
			return
		}
		io.Copy(io.Discard, response.Body)
		response.Body.Close()
		http.Error(w, "本地假代理拒绝连接", http.StatusBadGateway)
	})
	if err := http.ListenAndServe(fmt.Sprintf("127.0.0.1:%d", decoded.MixedPort), handler); err != nil {
		t.Fatal(err)
	}
}

func TestRunJob仅测延迟全部失败回报失败(t *testing.T) {
	var requests atomic.Int32
	newLifecycleTestCore(t, func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var result wsMsg
	runJob(ctx, wsMsg{JobID: "latency-failed", ClashConfig: lifecycleTestProxy, LatencyOnly: true}, func(message wsMsg) error {
		result = message
		return nil
	})
	if got := requests.Load(); got != 1+cfLatencySamples {
		t.Fatalf("应完成出口 IP 与全部延迟尝试，实际请求数=%d", got)
	}
	if result.Status != "failed" || result.LatencyMs != -1 || !strings.Contains(result.Error, "未获得有效延迟样本") {
		t.Fatalf("全部延迟失败未回报失败: %#v", result)
	}
}

func TestRunNodeTest仅测延迟保留父任务取消(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var requests atomic.Int32
	core := newLifecycleTestCore(t, func(w http.ResponseWriter, _ *http.Request) {
		if requests.Add(1) == 2 {
			// 首次为出口探测，第二次进入延迟探测后再取消，覆盖内核已就绪的路径。
			cancel()
		}
		w.WriteHeader(http.StatusBadGateway)
	})
	_, err := RunNodeTest(ctx, proxyRuntime{Core: coreMihomo, Bin: core}, lifecycleTestProxy, Options{LatencyOnly: true})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("延迟测速未透传父任务取消: %v", err)
	}
}

func TestConnectAndServe断线取消该连接派发的Run(t *testing.T) {
	if len(runJobSlots) != 0 {
		t.Fatal("测试开始时仍有测速任务在途")
	}
	requestStarted := make(chan struct{})
	releaseRequests := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseRequests) }) }
	var requests atomic.Int32
	newLifecycleTestCore(t, func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			close(requestStarted)
		}
		select {
		case <-r.Context().Done():
		case <-releaseRequests:
		}
		w.WriteHeader(http.StatusBadGateway)
	})
	// 失败路径也放行控制端请求，让旧实现能结束假内核而不留下后台任务。
	defer func() {
		release()
		waitForSlotCount(t, runJobSlots, 0, 5*time.Second, "测速任务槽")
	}()
	disconnect := make(chan struct{})
	var disconnectOnce sync.Once
	closeConnection := func() { disconnectOnce.Do(func() { close(disconnect) }) }
	handlerErr := make(chan error, 1)
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			handlerErr <- err
			return
		}
		defer conn.Close()
		var hello wsMsg
		if err := conn.ReadJSON(&hello); err != nil {
			handlerErr <- err
			return
		}
		if err := conn.WriteJSON(wsMsg{Type: "run", JobID: "connection-owned", ClashConfig: lifecycleTestProxy, LatencyOnly: true}); err != nil {
			handlerErr <- err
			return
		}
		<-disconnect
	}))
	defer func() {
		closeConnection()
		server.Close()
	}()
	serveDone := make(chan error, 1)
	go func() {
		serveDone <- connectAndServeWithIPv6Check("ws"+strings.TrimPrefix(server.URL, "http"), "local-test", nil, func() bool { return false })
	}()
	select {
	case <-requestStarted:
	case err := <-handlerErr:
		t.Fatal(err)
	case <-time.After(5 * time.Second):
		t.Fatal("测速任务未进入本地代理请求")
	}
	closeConnection()
	select {
	case err := <-serveDone:
		if err == nil {
			t.Fatal("主控断线后应返回连接错误")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("主控断线后连接未退出")
	}
	waitForSlotCount(t, runJobSlots, 0, 2*time.Second, "测速任务槽")
	if got := requests.Load(); got != 1 {
		t.Fatalf("断线后旧任务仍发起后续请求: %d", got)
	}
}
