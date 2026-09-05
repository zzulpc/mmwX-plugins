package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// 所有请求都由本地假代理响应，验证真实 HTTP 状态处理而不依赖公网探测端点。
func TestLatencyProbe仅接受204且不跟随跳转(t *testing.T) {
	for _, code := range []int{204, 200, 201, 206, 302, 502} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			var redirects atomic.Int32
			proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/redirected" {
					redirects.Add(1)
					w.WriteHeader(http.StatusNoContent)
					return
				}
				if code == http.StatusFound {
					w.Header().Set("Location", "http://probe.example/redirected")
				}
				w.WriteHeader(code)
				if code != http.StatusNoContent {
					_, _ = io.WriteString(w, "<html>登录或错误页面</html>")
				}
			}))
			defer proxy.Close()
			client := latencyProbeClient(testServerPort(t, proxy.URL), time.Second)
			defer client.CloseIdleConnections()
			wantValid := code == http.StatusNoContent
			if got := measureLatencySample(context.Background(), client, "http://probe.example/generate_204"); (got >= 0) != wantValid {
				t.Errorf("单样本 HTTP %d 的判定不符: %d", code, got)
			}
			if got := measureLatencySamples(context.Background(), client, "http://probe.example/generate_204", cfLatencySamples); (got >= 0) != wantValid {
				t.Errorf("多样本 HTTP %d 的判定不符: %d", code, got)
			}
			if redirects.Load() != 0 {
				t.Fatal("探测跟随了跳转，可能把其它页面当成原端点")
			}
		})
	}
}

func TestLatencyProbe多采样丢弃无效响应(t *testing.T) {
	var requests atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if requests.Add(1) == 2 {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "<html>错误页</html>")
	}))
	defer proxy.Close()
	client := latencyProbeClient(testServerPort(t, proxy.URL), time.Second)
	defer client.CloseIdleConnections()
	if got := measureLatencySamples(context.Background(), client, "http://probe.example/generate_204", 3); got < 0 {
		t.Fatal("混合响应中的有效 204 样本被丢弃")
	}
	if requests.Load() != 3 {
		t.Fatalf("未完成全部采样: %d", requests.Load())
	}
}

type latencyTestTransport func(*http.Request) (*http.Response, error)

func (f latencyTestTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

// 自定义响应体只用于模拟读取失败；正常 HTTP 的 204 会被标准库直接解释为空体。
type latencyTestBody struct {
	read   func([]byte) (int, error)
	closed bool
}

func (body *latencyTestBody) Read(p []byte) (int, error) { return body.read(p) }
func (body *latencyTestBody) Close() error {
	body.closed = true
	return nil
}

func TestLatencyProbe拒绝异常响应体并保留取消(t *testing.T) {
	for _, name := range []string{"读取失败", "非空响应体", "读取时取消"} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			body := &latencyTestBody{read: func(p []byte) (int, error) {
				switch name {
				case "读取失败":
					return 0, errors.New("本地模拟断流")
				case "读取时取消":
					cancel()
					return 0, io.EOF
				default:
					return strings.NewReader("异常内容").Read(p)
				}
			}}
			client := &http.Client{Transport: latencyTestTransport(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusNoContent, Body: body, Header: make(http.Header)}, nil
			})}
			if got := measureLatencySample(ctx, client, "http://probe.example/generate_204"); got >= 0 {
				t.Fatalf("异常样本被判为有效: %d", got)
			}
			if !body.closed {
				t.Fatal("失败样本未关闭响应体")
			}
		})
	}
}
