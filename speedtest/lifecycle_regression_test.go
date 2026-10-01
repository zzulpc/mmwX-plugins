package main

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func Test内核定位遵守已取消任务和锁等待(t *testing.T) {
	for _, tc := range []struct {
		name   string
		ensure func(context.Context) (string, error)
		lock   *kernelMutex
	}{{"mihomo", EnsureMihomo, mihomoMu}, {"sing-box", EnsureSingBox, singBoxMu}} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if _, err := tc.ensure(ctx); !errors.Is(err, context.Canceled) {
				t.Fatalf("已取消任务仍继续: %v", err)
			}
			tc.lock.Lock()
			defer tc.lock.Unlock()
			waitCtx, stop := context.WithTimeout(context.Background(), 30*time.Millisecond)
			defer stop()
			done := make(chan error, 1)
			go func() { _, err := tc.ensure(waitCtx); done <- err }()
			select {
			case err := <-done:
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("锁等待未传播截止时间: %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("取消不能打断定位锁等待")
			}
		})
	}
}

func Test内核版本检查继承调用期限(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("假版本程序使用 POSIX shell")
	}
	for _, tc := range []struct {
		name, env string
		ensure    func(context.Context) (string, error)
	}{
		{"mihomo", "MIHOMO_BIN", EnsureMihomo}, {"singbox", "SING_BOX_BIN", EnsureSingBox},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bin := filepath.Join(t.TempDir(), "slow-version")
			if err := os.WriteFile(bin, []byte("#!/bin/sh\nexec /bin/sleep 5\n"), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv(tc.env, bin)
			ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
			defer cancel()
			start := time.Now()
			_, err := tc.ensure(ctx)
			if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > time.Second {
				t.Fatalf("版本检查未及时取消: %v / %s", err, time.Since(start))
			}
		})
	}
}

func Test共享额度等待退款后继续读取(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(context.Canceled)
	q := &sharedDownloadQuota{maxBytes: 16, cancel: cancel}
	n, err := q.reserve(ctx, 8)
	if err != nil {
		t.Fatal(err)
	}
	reader := &sharedQuotaReader{ctx: ctx, source: strings.NewReader("abcdefghijklmnop"), quota: q}
	buf := make([]byte, 16)
	if n, err := reader.Read(buf); n != 8 || err != nil {
		t.Fatalf("首批数据: %d / %v", n, err)
	}
	done := make(chan error, 1)
	go func() {
		count, err := io.Copy(io.Discard, reader)
		if err == nil || errors.Is(err, errDownloadQuotaReached) || errors.Is(err, context.Canceled) {
			if count != 8 {
				err = errors.New("退款后未读满剩余字节")
			} else {
				err = nil
			}
		}
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("预留尚未结算就提前返回: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	q.finish(n, 0)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("退款没有唤醒等待者")
	}
	if q.total.Load() != 16 {
		t.Fatalf("实际读取 %d，未用满额度", q.total.Load())
	}
}

func Test共享额度等待可以取消(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	q := &sharedDownloadQuota{maxBytes: 8, cancel: cancel}
	if _, err := q.reserve(ctx, 8); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := q.reserve(ctx, 8); done <- err }()
	cancel(context.Canceled)
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("额度等待未取消")
	}
	q.finish(8, 0)
}

// 写侧失败时模拟下行仍存活；关闭通知代表阻塞读者会被唤醒。
type failedWebsocket struct {
	closed chan struct{}
	once   sync.Once
}

func (w *failedWebsocket) SetWriteDeadline(time.Time) error { return nil }
func (w *failedWebsocket) WriteMessage(int, []byte) error   { return io.ErrClosedPipe }
func (w *failedWebsocket) Close() error                     { w.once.Do(func() { close(w.closed) }); return nil }
func Test连接写失败同时关闭读侧(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	conn := &failedWebsocket{closed: make(chan struct{})}
	send := connectionSender(ctx, cancel, conn)
	if err := send(wsMsg{Type: "ping"}); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatal(err)
	}
	if ctx.Err() == nil {
		t.Fatal("写失败没有取消任务")
	}
	select {
	case <-conn.closed:
	default:
		t.Fatal("写失败没有唤醒读侧以触发重连")
	}
}
