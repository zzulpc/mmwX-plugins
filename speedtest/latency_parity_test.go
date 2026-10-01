package main

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRunNodeTest两种入口共用延迟口径且先于下载(t *testing.T) {
	endpoint, err := url.Parse(cfLatencyProbeURL)
	if err != nil {
		t.Fatal(err)
	}
	for _, latencyOnly := range []bool{true, false} {
		name := "完整测速"
		if latencyOnly {
			name = "只测延迟"
		}
		t.Run(name, func(t *testing.T) {
			var mu sync.Mutex
			var targets []string
			core := newLifecycleTestCore(t, func(w http.ResponseWriter, r *http.Request) {
				target := r.Header.Get("X-Test-Proxy-Target")
				mu.Lock()
				targets = append(targets, target)
				mu.Unlock()
				if r.Header.Get("X-Test-Proxy-Method") == http.MethodConnect {
					// 无需公网或真实 TLS：拒绝连接仍能验证三个采样实际经过同一端点。
					w.WriteHeader(http.StatusBadGateway)
					return
				}
				_, _ = io.CopyN(w, zeroReader{}, 1024)
			})
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			result, err := RunNodeTest(ctx, proxyRuntime{Core: coreMihomo, Bin: core}, lifecycleTestProxy, Options{
				LatencyOnly: latencyOnly, TestURL: "http://download.example/test", TestBytes: 1024,
				TestDuration: 50 * time.Millisecond,
			})
			if latencyOnly {
				if err == nil || !strings.Contains(err.Error(), "未获得有效延迟样本") {
					t.Fatalf("纯延迟任务应保留失败状态: %+v, %v", result, err)
				}
			} else if err != nil || result.Bytes != 1024 || result.DownMbps <= 0 {
				t.Fatalf("延迟不可测不应抹掉可用的下载结果: %+v, %v", result, err)
			}
			if result.LatencyMs != -1 {
				t.Fatalf("失败的延迟必须保留为不可测: %+v", result)
			}
			mu.Lock()
			defer mu.Unlock()
			wantCount := 1 + cfLatencySamples
			if !latencyOnly {
				wantCount++
			}
			if len(targets) != wantCount {
				t.Fatalf("请求次数不符: %v", targets)
			}
			for i := 1; i <= cfLatencySamples; i++ {
				if targets[i] != endpoint.Hostname()+":443" {
					t.Fatalf("第 %d 次延迟采样使用了不同端点，或下载抢在延迟之前: %v", i, targets)
				}
			}
			if !latencyOnly && targets[len(targets)-1] != "download.example" {
				t.Fatalf("下载没有排在延迟之后: %v", targets)
			}
		})
	}
}
