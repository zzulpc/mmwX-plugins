package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRunNodeTest等待锁计入超时(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	runMu.Lock()
	done := make(chan error, 1)
	go func() {
		_, err := RunNodeTest(ctx, proxyRuntime{}, `{}`, Options{Timeout: time.Second})
		done <- err
	}()
	<-ctx.Done()
	runMu.Unlock()

	err := <-done
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("等待锁超时应返回 context deadline exceeded，实际为 %v", err)
	}
}

func TestDownloadTimed多线程共享字节上限(t *testing.T) {
	const maxBytes int64 = 256 << 10
	var logs bytes.Buffer
	originalLogOutput := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(originalLogOutput) })
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = io.CopyN(w, zeroReader{}, maxBytes*4)
	}))
	defer proxy.Close()

	proxyURL, err := url.Parse(proxy.URL)
	if err != nil {
		t.Fatalf("解析测试代理地址失败: %v", err)
	}
	port, err := strconv.Atoi(proxyURL.Port())
	if err != nil {
		t.Fatalf("解析测试代理端口失败: %v", err)
	}

	got, _, err := downloadTimed(context.Background(), "http://download.example/test.bin", 5*time.Second, maxBytes, 8, minBufSize, port)
	if err != nil {
		t.Fatalf("多线程下载失败: %v", err)
	}
	if got > maxBytes {
		t.Fatalf("总下载量 %d 超过上限 %d", got, maxBytes)
	}
	if got != maxBytes {
		t.Fatalf("总下载量 %d，期望用满上限 %d", got, maxBytes)
	}
	if strings.Contains(logs.String(), "提前失败") {
		t.Fatalf("共享额度正常结束被误记为流失败: %s", logs.String())
	}
}

func TestDownloadTimed单线程首个响应EOF后重试成功(t *testing.T) {
	const maxBytes int64 = 128 << 10
	var attempts atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if attempts.Add(1) == 1 {
			hijacker, ok := w.(http.Hijacker)
			if !ok {
				t.Error("测试服务器不支持 Hijack")
				return
			}
			conn, _, err := hijacker.Hijack()
			if err != nil {
				t.Errorf("制造首次 EOF 失败: %v", err)
				return
			}
			_ = conn.Close()
			return
		}
		w.Header().Set("Content-Length", strconv.FormatInt(maxBytes, 10))
		_, _ = io.CopyN(w, zeroReader{}, maxBytes)
	}))
	defer proxy.Close()

	port := testServerPort(t, proxy.URL)
	got, _, err := downloadTimed(context.Background(), "http://download.example/test.bin", time.Second, maxBytes, 1, minBufSize, port)
	if err != nil {
		t.Fatalf("首个响应 EOF 后重试失败: %v", err)
	}
	if got != maxBytes {
		t.Fatalf("重试后下载量为 %d，期望 %d", got, maxBytes)
	}
	if attempts.Load() != 2 {
		t.Fatalf("请求次数为 %d，期望首次失败后只重试一次", attempts.Load())
	}
}

func TestSingleDownloadRetryDelay固定为250与750毫秒(t *testing.T) {
	first, ok := singleDownloadRetryDelay(1)
	if !ok || first != 250*time.Millisecond {
		t.Fatalf("第一次退避为 %s, ok=%t，期望 250ms", first, ok)
	}
	second, ok := singleDownloadRetryDelay(2)
	if !ok || second != 750*time.Millisecond {
		t.Fatalf("第二次退避为 %s, ok=%t，期望 750ms", second, ok)
	}
	if delay, ok := singleDownloadRetryDelay(3); ok || delay != 0 {
		t.Fatalf("第三次失败后仍返回退避 %s, ok=%t", delay, ok)
	}
}

func TestDownloadTimed单线程前两次EOF第三次成功且准备阶段不计时(t *testing.T) {
	const maxBytes int64 = 128 << 10
	var attempts atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if attempts.Add(1) <= 2 {
			hijacker, ok := w.(http.Hijacker)
			if !ok {
				t.Error("测试服务器不支持 Hijack")
				return
			}
			conn, _, err := hijacker.Hijack()
			if err != nil {
				t.Errorf("制造响应前 EOF 失败: %v", err)
				return
			}
			_ = conn.Close()
			return
		}
		w.Header().Set("Content-Length", strconv.FormatInt(maxBytes, 10))
		_, _ = io.CopyN(w, zeroReader{}, maxBytes)
	}))
	defer proxy.Close()

	port := testServerPort(t, proxy.URL)
	wallStart := time.Now()
	got, measured, err := downloadTimed(context.Background(), "http://download.example/test.bin", time.Second, maxBytes, 1, minBufSize, port)
	wallElapsed := time.Since(wallStart)
	if err != nil {
		t.Fatalf("前两次 EOF 后第三次下载失败: %v", err)
	}
	if got != maxBytes {
		t.Fatalf("第三次下载量为 %d，期望 %d", got, maxBytes)
	}
	if attempts.Load() != 3 {
		t.Fatalf("请求次数为 %d，期望前两次失败后第三次成功", attempts.Load())
	}
	if wallElapsed-measured < 900*time.Millisecond {
		t.Fatalf("两段退避疑似计入吞吐窗口: wall=%s measured=%s", wallElapsed, measured)
	}
}

func TestDownloadTimed单线程父Context在退避中取消(t *testing.T) {
	var attempts atomic.Int32
	firstAttemptDone := make(chan struct{})
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			t.Error("测试服务器不支持 Hijack")
			return
		}
		conn, _, err := hijacker.Hijack()
		if err != nil {
			t.Errorf("制造响应前 EOF 失败: %v", err)
			return
		}
		_ = conn.Close()
		select {
		case <-firstAttemptDone:
		default:
			close(firstAttemptDone)
		}
	}))
	defer proxy.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		<-firstAttemptDone
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	port := testServerPort(t, proxy.URL)
	_, _, err := downloadTimed(ctx, "http://download.example/test.bin", time.Second, 0, 1, minBufSize, port)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("父 context 在退避中取消应立即返回 context canceled，实际为 %v", err)
	}
	if attempts.Load() != 1 {
		t.Fatalf("取消后仍发出了后续请求: %d", attempts.Load())
	}
}

func TestDownloadTimed单线程HTTP错误不重试(t *testing.T) {
	var attempts atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		http.Error(w, "禁止访问", http.StatusForbidden)
	}))
	defer proxy.Close()

	port := testServerPort(t, proxy.URL)
	_, _, err := downloadTimed(context.Background(), "http://download.example/test.bin", time.Second, 0, 1, minBufSize, port)
	if err == nil || !strings.Contains(err.Error(), "HTTP 403") {
		t.Fatalf("HTTP 403 应作为确定性错误直接返回，实际为 %v", err)
	}
	if attempts.Load() != 1 {
		t.Fatalf("HTTP 403 被错误重试，请求次数为 %d", attempts.Load())
	}
}

func TestDownloadTimed单线程中途断流后共享剩余额度(t *testing.T) {
	const (
		maxBytes  int64 = 192 << 10
		firstPart int64 = 64 << 10
	)
	var attempts atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if attempts.Add(1) == 1 {
			w.Header().Set("Content-Length", strconv.FormatInt(maxBytes, 10))
			_, _ = io.CopyN(w, zeroReader{}, firstPart)
			return
		}
		remaining := maxBytes - firstPart
		w.Header().Set("Content-Length", strconv.FormatInt(remaining, 10))
		_, _ = io.CopyN(w, zeroReader{}, remaining)
	}))
	defer proxy.Close()

	port := testServerPort(t, proxy.URL)
	got, _, err := downloadTimed(context.Background(), "http://download.example/test.bin", time.Second, maxBytes, 1, minBufSize, port)
	if err != nil {
		t.Fatalf("中途断流后的串行重试失败: %v", err)
	}
	if got != maxBytes {
		t.Fatalf("两次请求累计下载量为 %d，期望严格等于共享额度 %d", got, maxBytes)
	}
	if attempts.Load() != 2 {
		t.Fatalf("请求次数为 %d，期望 2", attempts.Load())
	}
}

func TestDownloadTimed单线程响应体中断后立即重试不耗尽测量窗口(t *testing.T) {
	const (
		maxBytes        int64 = 128 << 10
		firstPart       int64 = 64 << 10
		measureDuration       = 120 * time.Millisecond
	)
	var attempts atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if attempts.Add(1) == 1 {
			w.Header().Set("Content-Length", strconv.FormatInt(maxBytes, 10))
			_, _ = io.CopyN(w, zeroReader{}, firstPart)
			return
		}
		remaining := maxBytes - firstPart
		w.Header().Set("Content-Length", strconv.FormatInt(remaining, 10))
		_, _ = io.CopyN(w, zeroReader{}, remaining)
	}))
	defer proxy.Close()

	port := testServerPort(t, proxy.URL)
	got, _, err := downloadTimed(context.Background(), "http://download.example/test.bin", measureDuration, maxBytes, 1, minBufSize, port)
	if err != nil {
		t.Fatalf("响应体中断后的立即重试失败: %v", err)
	}
	if got != maxBytes {
		t.Fatalf("两次响应累计下载量为 %d，期望 %d", got, maxBytes)
	}
	if attempts.Load() != 2 {
		t.Fatalf("请求次数为 %d，期望响应体中断后立即重试一次", attempts.Load())
	}
}

func TestDownloadTimed单线程响应准备不占用吞吐窗口(t *testing.T) {
	const setupDelay = 120 * time.Millisecond
	const measureDuration = 80 * time.Millisecond
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(setupDelay)
		w.WriteHeader(http.StatusOK)
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Error("测试服务器不支持 Flush")
			return
		}
		flusher.Flush()
		chunk := make([]byte, 4<<10)
		for {
			select {
			case <-r.Context().Done():
				return
			default:
				if _, err := w.Write(chunk); err != nil {
					return
				}
				flusher.Flush()
				time.Sleep(time.Millisecond)
			}
		}
	}))
	defer proxy.Close()

	port := testServerPort(t, proxy.URL)
	wallStart := time.Now()
	got, measured, err := downloadTimed(context.Background(), "http://download.example/test.bin", measureDuration, 0, 1, minBufSize, port)
	wallElapsed := time.Since(wallStart)
	if err != nil {
		t.Fatalf("延迟响应后的测速失败: %v", err)
	}
	if got == 0 {
		t.Fatal("延迟响应后的吞吐窗口未收到数据")
	}
	if measured < measureDuration/2 || measured > measureDuration+150*time.Millisecond {
		t.Fatalf("测速窗口为 %s，期望接近 %s", measured, measureDuration)
	}
	if wallElapsed-measured < setupDelay/2 {
		t.Fatalf("响应准备时间疑似仍计入吞吐窗口: wall=%s measured=%s", wallElapsed, measured)
	}
}

func TestDownloadTimed单线程重试未收到响应不能借前次字节成功(t *testing.T) {
	const partialBytes int64 = 64 << 10
	const measureDuration = 80 * time.Millisecond
	var attempts atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if attempts.Add(1) == 1 {
			w.Header().Set("Content-Length", strconv.FormatInt(partialBytes*2, 10))
			_, _ = io.CopyN(w, zeroReader{}, partialBytes)
			return
		}
		<-r.Context().Done()
	}))
	defer proxy.Close()

	port := testServerPort(t, proxy.URL)
	_, _, err := downloadTimed(context.Background(), "http://download.example/test.bin", measureDuration, 0, 1, minBufSize, port)
	if err == nil {
		t.Fatal("第二次请求未收到响应头时不应借首次部分字节成功")
	}
	if attempts.Load() != 2 {
		t.Fatalf("请求次数为 %d，期望严格限制为 2", attempts.Load())
	}
}

func TestDownloadTimed单线程三次响应体均断流仍失败(t *testing.T) {
	const partialBytes int64 = 64 << 10
	var attempts atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		w.Header().Set("Content-Length", strconv.FormatInt(partialBytes*2, 10))
		_, _ = io.CopyN(w, zeroReader{}, partialBytes)
	}))
	defer proxy.Close()

	port := testServerPort(t, proxy.URL)
	_, _, err := downloadTimed(context.Background(), "http://download.example/test.bin", 3*time.Second, 0, 1, minBufSize, port)
	if err == nil || !strings.Contains(err.Error(), "重试仍失败") {
		t.Fatalf("三次响应体均提前断流时必须失败，实际为 %v", err)
	}
	if attempts.Load() != 3 {
		t.Fatalf("请求次数为 %d，期望严格限制为 3", attempts.Load())
	}
}

func TestDownloadTimed父任务超时不能按测速截止成功(t *testing.T) {
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Error("测试服务器不支持 Flush")
			return
		}
		flusher.Flush()
		chunk := make([]byte, 4<<10)
		for {
			select {
			case <-r.Context().Done():
				return
			default:
				if _, err := w.Write(chunk); err != nil {
					return
				}
				flusher.Flush()
				time.Sleep(time.Millisecond)
			}
		}
	}))
	defer proxy.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	port := testServerPort(t, proxy.URL)
	_, _, err := downloadTimed(ctx, "http://download.example/test.bin", time.Second, 0, 8, minBufSize, port)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("父任务超时必须失败，实际为 %v", err)
	}
}

func TestDownloadTimed部分流完成后父任务超时仍失败(t *testing.T) {
	var attempts atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if attempts.Add(1) == 1 {
			w.Header().Set("Content-Length", "4096")
			_, _ = io.CopyN(w, zeroReader{}, 4096)
			return
		}
		<-r.Context().Done()
	}))
	defer proxy.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	port := testServerPort(t, proxy.URL)
	_, _, err := downloadTimed(ctx, "http://download.example/test.bin", time.Second, 0, 8, minBufSize, port)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("已有一条流完成时父任务超时仍必须失败，实际为 %v", err)
	}
	if attempts.Load() != 8 {
		t.Fatalf("请求次数为 %d，期望 8 条流全部启动", attempts.Load())
	}
}

func TestDownloadTimed请求构造错误不泄漏URL(t *testing.T) {
	const secret = "不应出现在错误里的签名"
	_, _, err := downloadTimed(context.Background(), "://download.example/test.bin?token="+secret, time.Second, 0, 1, minBufSize, 1)
	if err == nil {
		t.Fatal("非法下载 URL 应返回错误")
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "download.example") {
		t.Fatalf("请求构造错误泄漏了原始 URL: %v", err)
	}
}

func TestDownloadTimed单线程连续响应EOF仍失败(t *testing.T) {
	var attempts atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			t.Error("测试服务器不支持 Hijack")
			return
		}
		conn, _, err := hijacker.Hijack()
		if err != nil {
			t.Errorf("制造 EOF 失败: %v", err)
			return
		}
		_ = conn.Close()
	}))
	defer proxy.Close()

	port := testServerPort(t, proxy.URL)
	_, _, err := downloadTimed(context.Background(), "http://download.example/test.bin?token=不应出现在错误里", time.Second, 0, 1, minBufSize, port)
	if err == nil || !strings.Contains(err.Error(), "重试仍失败") {
		t.Fatalf("连续 EOF 应在两次重试后失败，实际为 %v", err)
	}
	if strings.Contains(err.Error(), "token=") || strings.Contains(err.Error(), "download.example") {
		t.Fatalf("下载错误回显了完整 URL: %v", err)
	}
	if attempts.Load() != 3 {
		t.Fatalf("连续 EOF 的请求次数为 %d，期望严格限制为 3", attempts.Load())
	}
}

func TestDownloadTimed多线程全部中途断流不再误报成功(t *testing.T) {
	const partialBytes int64 = 64 << 10
	var attempts atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		w.Header().Set("Content-Length", strconv.FormatInt(partialBytes*2, 10))
		_, _ = io.CopyN(w, zeroReader{}, partialBytes)
	}))
	defer proxy.Close()

	port := testServerPort(t, proxy.URL)
	_, _, err := downloadTimed(context.Background(), "http://download.example/test.bin", time.Second, 0, 8, minBufSize, port)
	if err == nil {
		t.Fatal("8 条流全部提前断开时不应仅因读到部分字节而成功")
	}
	if attempts.Load() != 8 {
		t.Fatalf("多线程请求次数为 %d，期望保持 8 且不额外重试", attempts.Load())
	}
}

func testServerPort(t *testing.T, serverURL string) int {
	t.Helper()
	proxyURL, err := url.Parse(serverURL)
	if err != nil {
		t.Fatalf("解析测试代理地址失败: %v", err)
	}
	port, err := strconv.Atoi(proxyURL.Port())
	if err != nil {
		t.Fatalf("解析测试代理端口失败: %v", err)
	}
	return port
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 0
	}
	return len(p), nil
}
