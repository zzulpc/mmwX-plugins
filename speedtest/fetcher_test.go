package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestFetchSubscription正常跳转与响应原样回传(t *testing.T) {
	body := []byte{0xff, 0xfe, 0x00, 'a', '\n'}
	for _, status := range []int{http.StatusOK, http.StatusForbidden} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.Header.Get("User-Agent") != "Custom-Subscription-Client/1.0" {
					t.Errorf("跳转前后都应保留自定义 UA: %q", r.Header.Get("User-Agent"))
				}
				if r.URL.Path == "/redirect" {
					http.Redirect(w, r, "/subscription?token=test-only", http.StatusFound)
					return
				}
				w.Header().Set("subscription-userinfo", "upload=1; download=2; total=3")
				w.Header().Set("content-disposition", "attachment; filename=subscription.yaml")
				w.Header().Set("profile-update-interval", "24")
				w.Header().Set("Content-Type", "application/octet-stream")
				w.Header().Set("Set-Cookie", "private-session=hidden")
				w.WriteHeader(status)
				_, _ = w.Write(body)
			}))
			defer server.Close()
			got := fetchSubscription(context.Background(), wsMsg{JobID: "fetch-1", URL: server.URL + "/redirect",
				UserAgent: "Custom-Subscription-Client/1.0"}, server.Client())
			decoded, err := base64.StdEncoding.DecodeString(got.Body)
			if err != nil || !bytes.Equal(decoded, body) || got.Type != "fetch_result" || got.JobID != "fetch-1" || got.Status != "ok" || got.StatusCode != status {
				t.Fatalf("HTTP 状态或二进制内容未完整回传: %+v, %v", got, err)
			}
			if requests.Load() != 2 || len(got.Headers) != 4 || got.Headers["subscription-userinfo"] == "" || got.Headers["Set-Cookie"] != "" {
				t.Fatalf("跳转或响应头不符: 请求=%d，响应头=%v", requests.Load(), got.Headers)
			}
		})
	}
}

func TestFetchSubscription默认UA与压缩响应(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") != "clash-meta/2.4.0" {
			t.Errorf("默认 UA 不兼容上游: %q", r.Header.Get("User-Agent"))
		}
		w.Header().Set("Content-Encoding", "gzip")
		compressed := gzip.NewWriter(w)
		_, _ = io.WriteString(compressed, "proxies: []")
		_ = compressed.Close()
	}))
	defer server.Close()
	got := fetchSubscription(context.Background(), wsMsg{URL: server.URL}, server.Client())
	body, err := base64.StdEncoding.DecodeString(got.Body)
	if err != nil || got.Status != "ok" || string(body) != "proxies: []" {
		t.Fatalf("压缩订阅未正常解码: %+v, %v", got, err)
	}
}

func TestFetchSubscription大小边界与大回包(t *testing.T) {
	for _, size := range []int64{fetchMaxBytes, fetchMaxBytes + 1} {
		t.Run(strconv.FormatInt(size, 10), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
				w.(http.Flusher).Flush() // 分块传输，不能依赖 Content-Length 判断上限。
				_, _ = io.CopyN(w, zeroReader{}, size)
			}))
			defer server.Close()
			got := fetchSubscription(context.Background(), wsMsg{JobID: "large-subscription", URL: server.URL}, server.Client())
			if size > fetchMaxBytes {
				if got.Status != "failed" || got.Body != "" || !strings.Contains(got.Error, "16 MiB") {
					t.Fatalf("超限响应不能被静默截断为成功: status=%s error=%s", got.Status, got.Error)
				}
				return
			}
			decoded, err := base64.StdEncoding.DecodeString(got.Body)
			if err != nil || got.Status != "ok" || int64(len(decoded)) != size {
				t.Fatalf("恰好达到上限的订阅应正常通过: status=%s size=%d error=%v", got.Status, len(decoded), err)
			}
			// 4 MiB 是主控发给测速端的输入限制，不能误用于反方向的 base64 大回包。
			roundTripFetchResult(t, got)
		})
	}
}

func roundTripFetchResult(t *testing.T, result wsMsg) {
	t.Helper()
	sent := make(chan error, 1)
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			sent <- err
			return
		}
		defer conn.Close()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		sent <- connectionSender(ctx, cancel, conn)(result)
	}))
	defer server.Close()
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	var got wsMsg
	if err := conn.ReadJSON(&got); err != nil {
		t.Fatal(err)
	}
	if err := <-sent; err != nil {
		t.Fatal(err)
	}
	if got.Body != result.Body || got.JobID != result.JobID || got.Type != "fetch_result" {
		t.Fatal("WebSocket 回包丢失内容或任务关联")
	}
}

func TestFetchSubscription拒绝非法URL且错误不含令牌(t *testing.T) {
	client := &http.Client{Transport: latencyTestTransport(func(r *http.Request) (*http.Response, error) {
		return nil, &url.Error{Op: "Get", URL: "https://redirect.example/?token=redirect-secret", Err: errors.New("network error")}
	})}
	for _, target := range []string{"", "file:///tmp/private", "ftp://example.com/sub", "http://", "://broken?token=source-secret", "https://example.com/sub?token=source-secret"} {
		got := fetchSubscription(context.Background(), wsMsg{URL: target}, client)
		if got.Status != "failed" || strings.Contains(got.Error, "secret") || strings.Contains(got.Error, "example.com") {
			t.Fatalf("请求失败信息不应暴露地址或令牌: %+v", got)
		}
	}
}

func TestFetchClient连接前拒绝内网且不继承探测放行(t *testing.T) {
	t.Setenv(probeAllowPrivateEnv, "1")
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	client := newFetchClient()
	defer client.CloseIdleConnections()
	for _, target := range []string{server.URL, strings.Replace(server.URL, "127.0.0.1", "localhost", 1)} {
		got := fetchSubscription(context.Background(), wsMsg{URL: target}, client)
		if got.Status != "failed" || !strings.Contains(got.Error, "内网") {
			t.Fatalf("实际拨号未拦截内网目标: %+v", got)
		}
	}
	if hits.Load() != 0 {
		t.Fatal("拒绝之前已经向内网发出 HTTP 请求")
	}
	for _, address := range []string{"[::1%lo0]:80", "[::ffff:127.0.0.1]:80", "169.254.169.254:80", "100.64.0.1:80", "192.0.0.1:80", "0.0.0.1:80", "broken"} {
		if !errors.Is(controlFetchTarget("tcp", address, nil), errFetchTargetNotPublic) {
			t.Errorf("地址应被拒绝: %s", address)
		}
	}
	for _, address := range []string{"1.1.1.1:8443", "[2606:4700::1111]:8080"} {
		if err := controlFetchTarget("tcp", address, nil); err != nil {
			t.Errorf("合法公网地址及非标准端口应通过纯地址检查: %s, %v", address, err)
		}
	}
}

func TestFetchClient重定向重新检查地址且直连(t *testing.T) {
	var hits atomic.Int32
	internal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
	}))
	defer internal.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, internal.URL+"?token=private-token", http.StatusFound)
	}))
	defer redirect.Close()
	t.Setenv("HTTP_PROXY", internal.URL)
	client := newFetchClient()
	defer client.CloseIdleConnections()
	transport := client.Transport.(*http.Transport)
	guardedDial := transport.DialContext
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		if address == "subscription.example:80" {
			// 只把测试中的首跳映射到本地服务，重定向仍走生产地址检查。
			return (&net.Dialer{}).DialContext(ctx, network, strings.TrimPrefix(redirect.URL, "http://"))
		}
		if address != strings.TrimPrefix(internal.URL, "http://") {
			return nil, errors.New("测试禁止访问非本地目标")
		}
		return guardedDial(ctx, network, address)
	}
	got := fetchSubscription(context.Background(), wsMsg{URL: "http://subscription.example/sub"}, client)
	if got.Status != "failed" || !strings.Contains(got.Error, "内网") || strings.Contains(got.Error, "private-token") || hits.Load() != 0 {
		t.Fatalf("跳转或环境代理绕过了地址检查: %+v, 请求=%d", got, hits.Load())
	}
}

func TestDispatchFetchJob支持小批量且不因测速占用拒绝(t *testing.T) {
	assertFetchSlotsEmpty(t)
	occupyRunExecution(t)
	started := make(chan struct{}, fetchConcurrency)
	release := make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	var active, peak atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		current := active.Add(1)
		defer active.Add(-1)
		for old := peak.Load(); current > old; old = peak.Load() {
			if peak.CompareAndSwap(old, current) {
				break
			}
		}
		select {
		case started <- struct{}{}:
		default:
		}
		select {
		case <-release:
			_, _ = io.WriteString(w, "proxies: []")
		case <-r.Context().Done():
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	results := make(chan wsMsg, fetchMaxInflightJobs+1)
	send := func(m wsMsg) error { results <- m; return nil }
	for i := 0; i < fetchMaxInflightJobs; i++ {
		dispatchFetchJobWithClient(ctx, wsMsg{JobID: strconv.Itoa(i), URL: server.URL}, send, server.Client(), 5*time.Second)
	}
	for i := 0; i < fetchConcurrency; i++ {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("抓取被测速占用或没有并发执行")
		}
	}
	dispatchFetchJobWithClient(ctx, wsMsg{JobID: "overflow", URL: server.URL}, send, server.Client(), 5*time.Second)
	if got := <-results; got.JobID != "overflow" || got.Status != "failed" {
		t.Fatalf("超出在途容量应立即说明可重试: %+v", got)
	}
	once.Do(func() { close(release) })
	for i := 0; i < fetchMaxInflightJobs; i++ {
		select {
		case got := <-results:
			if got.Status != "ok" {
				t.Errorf("小批量任务失败: %+v", got)
			}
		case <-time.After(time.Second):
			t.Fatal("小批量任务未正常完成")
		}
	}
	waitForSlotCount(t, fetchJobSlots, 0, time.Second, "抓取任务槽")
	waitForSlotCount(t, fetchSlots, 0, time.Second, "抓取下载槽")
	if peak.Load() != fetchConcurrency {
		t.Fatalf("进程并发不符: %d", peak.Load())
	}
}

func TestDispatchFetchJob取消下载与排队超时释放槽位(t *testing.T) {
	t.Run("下载中取消", func(t *testing.T) {
		assertFetchSlotsEmpty(t)
		started := make(chan struct{})
		stopped := make(chan struct{})
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			w.(http.Flusher).Flush()
			close(started)
			<-r.Context().Done()
			close(stopped)
		}))
		defer server.Close()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		var replies atomic.Int32
		dispatchFetchJobWithClient(ctx, wsMsg{URL: server.URL}, func(wsMsg) error { replies.Add(1); return nil }, server.Client(), 5*time.Second)
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("抓取未启动")
		}
		cancel()
		select {
		case <-stopped:
		case <-time.After(time.Second):
			t.Fatal("取消没有终止 HTTP 下载")
		}
		waitForSlotCount(t, fetchJobSlots, 0, time.Second, "抓取任务槽")
		assertFetchSlotsEmpty(t)
		if replies.Load() != 0 {
			t.Fatal("已断开的连接仍收到结果")
		}
	})
	t.Run("排队超时", func(t *testing.T) {
		assertFetchSlotsEmpty(t)
		for i := 0; i < fetchConcurrency; i++ {
			fetchSlots <- struct{}{}
		}
		defer func() {
			for i := 0; i < fetchConcurrency; i++ {
				<-fetchSlots
			}
		}()
		replies := make(chan wsMsg, 1)
		dispatchFetchJobWithClient(context.Background(), wsMsg{JobID: "queued"}, func(m wsMsg) error { replies <- m; return nil }, newFetchClient(), 20*time.Millisecond)
		select {
		case got := <-replies:
			if got.JobID != "queued" || got.Status != "failed" || !strings.Contains(got.Error, "等待") {
				t.Fatalf("排队超时结果不符: %+v", got)
			}
		case <-time.After(time.Second):
			t.Fatal("排队任务没有按期限退出")
		}
		waitForSlotCount(t, fetchJobSlots, 0, time.Second, "抓取任务槽")
	})
}

func TestConnectAndServe声明抓取能力且分派请求(t *testing.T) {
	assertFetchSlotsEmpty(t)
	upgrader := websocket.Upgrader{}
	received := make(chan wsMsg, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		var hello wsMsg
		if err := conn.ReadJSON(&hello); err != nil {
			t.Error(err)
			return
		}
		caps := strings.Join(hello.Caps, ",")
		if !strings.Contains(caps, "fetch") || strings.Contains(caps, "update") {
			t.Errorf("能力声明不符: %v", hello.Caps)
		}
		if err := conn.WriteJSON(wsMsg{Type: "fetch", JobID: "ws-fetch", URL: "file:///not-allowed"}); err != nil {
			t.Error(err)
			return
		}
		var result wsMsg
		if err := conn.ReadJSON(&result); err != nil {
			t.Error(err)
			return
		}
		received <- result
	}))
	defer server.Close()
	_ = connectAndServeWithIPv6Check("ws"+strings.TrimPrefix(server.URL, "http"), "test", nil, func() bool { return false })
	select {
	case got := <-received:
		if got.Type != "fetch_result" || got.JobID != "ws-fetch" || got.Status != "failed" {
			t.Fatalf("主控请求未正确分派并回包: %+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("未收到抓取回包")
	}
	waitForSlotCount(t, fetchJobSlots, 0, time.Second, "抓取任务槽")
}

func TestFetchSubscription超时与读取失败(t *testing.T) {
	for _, failure := range []error{context.DeadlineExceeded, io.ErrUnexpectedEOF} {
		client := &http.Client{Transport: latencyTestTransport(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Header: make(http.Header), Body: &latencyTestBody{read: func([]byte) (int, error) { return 0, failure }}}, nil
		})}
		got := fetchSubscription(context.Background(), wsMsg{URL: "https://subscription.example/?token=secret"}, client)
		if got.Status != "failed" || got.Body != "" || strings.Contains(got.Error, "secret") {
			t.Fatalf("读取失败被当作有效订阅: %+v", got)
		}
		if errors.Is(failure, context.DeadlineExceeded) && !strings.Contains(got.Error, "超时") {
			t.Fatalf("超时原因丢失: %+v", got)
		}
	}
}

func assertFetchSlotsEmpty(t *testing.T) {
	t.Helper()
	if jobs, downloads := len(fetchJobSlots), len(fetchSlots); jobs != 0 || downloads != 0 {
		t.Fatalf("抓取资源未释放: jobs=%d downloads=%d", jobs, downloads)
	}
}
