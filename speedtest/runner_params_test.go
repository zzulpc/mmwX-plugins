package main

import (
	"math"
	"reflect"
	"testing"
	"time"
)

// clampSpeedTestParams 是家用测速端的 OOM 闸门:峰值内存约等于
// BufSize × Threads × downloadBuffersPerThread，而 BufSize/Threads 都由主控在任务里下发。
// 倍率那一项是后补的:每条流除了 io.CopyBuffer 的 buffer，还各带一份同样大的
// http.Transport ReadBufferSize，原来只算一份,实际峰值是闸门的两倍。
func TestClampSpeedTestParams归一到文档中的默认值(t *testing.T) {
	tests := []struct {
		name        string
		bufSize     int
		threads     int
		wantBufSize int
		wantThreads int
	}{
		{"全部留空回落默认", 0, 0, defaultBufSize, 1},
		{"负数回落默认", -1, -8, defaultBufSize, 1},
		{"低于下限抬到 64KB", 1024, 1, minBufSize, 1},
		{"高于上限压到 16MB", 64 << 20, 1, maxBufSize, 1},
		{"线程数封顶", defaultBufSize, 1000, defaultBufSize, maxSpeedThreads},
		{"正常取值原样保留", 4 << 20, 8, 4 << 20, 8},
		{"峰值内存超标时缩小 buf", maxBufSize, maxSpeedThreads,
			maxSpeedTotalMem / (maxSpeedThreads * downloadBuffersPerThread), maxSpeedThreads},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			bufSize, threads := clampSpeedTestParams(test.bufSize, test.threads)
			if bufSize != test.wantBufSize || threads != test.wantThreads {
				t.Fatalf("clamp(%d, %d) = (%d, %d)，预期 (%d, %d)",
					test.bufSize, test.threads, bufSize, threads, test.wantBufSize, test.wantThreads)
			}
		})
	}
}

// 主控下发的是未经校验的值，所以闸门必须对任意输入都成立，而不只是对上面那张表。
func TestClampSpeedTestParams对任意输入都守住峰值内存(t *testing.T) {
	values := []int{math.MinInt, -1 << 20, -1, 0, 1, 1024, minBufSize, defaultBufSize,
		maxBufSize, maxSpeedTotalMem, math.MaxInt}
	threadValues := []int{math.MinInt, -1, 0, 1, 2, 8, maxSpeedThreads, 1 << 20, math.MaxInt}

	for _, rawBuf := range values {
		for _, rawThreads := range threadValues {
			bufSize, threads := clampSpeedTestParams(rawBuf, rawThreads)
			if threads < 1 || threads > maxSpeedThreads {
				t.Fatalf("clamp(%d, %d) 的线程数越界: %d", rawBuf, rawThreads, threads)
			}
			if bufSize < minBufSize || bufSize > maxBufSize {
				t.Fatalf("clamp(%d, %d) 的 buf 越界: %d", rawBuf, rawThreads, bufSize)
			}
			if peak := int64(bufSize) * int64(threads) * downloadBuffersPerThread; peak > maxSpeedTotalMem {
				t.Fatalf("clamp(%d, %d) 的峰值内存 %d 超过上限 %d", rawBuf, rawThreads, peak, maxSpeedTotalMem)
			}
		}
	}
}

func TestSortInt64Asc(t *testing.T) {
	tests := []struct {
		input []int64
		want  []int64
	}{
		{input: nil, want: nil},
		{input: []int64{1}, want: []int64{1}},
		{input: []int64{2, 1}, want: []int64{1, 2}},
		{input: []int64{5, 3, 9, 1, 3}, want: []int64{1, 3, 3, 5, 9}},
		{input: []int64{-1, -5, 0, 3}, want: []int64{-5, -1, 0, 3}},
	}
	for _, test := range tests {
		values := append([]int64(nil), test.input...)
		sortInt64Asc(values)
		if !reflect.DeepEqual(values, test.want) {
			t.Fatalf("排序结果不符: 输入 %v，实际 %v，预期 %v", test.input, values, test.want)
		}
	}
}

// runExecutionBudget 是「取得执行权之后」的执行预算，必须能装下最慢那条路径上每个阶段的
// 超时上限。历史 bug:预算写死 38s，而阶段上限之和是 51~61s —— 慢但能用的节点会在最后一步
// 撞上总预算，报出「剩余时间不足」这种看起来像节点故障、实际是自家算错了的错误。
// 调整任何一个阶段超时都必须让这个用例重新过一遍。
func TestRun执行预算能装下所有阶段超时(t *testing.T) {
	// 下载测速最慢路径:sing-box check → 内核就绪 → 出口 IP → 延迟 → 下载准备 → 吞吐窗口。
	download := singBoxCheckTimeout + coreReadyTimeout + egressProbeTimeout +
		latencyProbeTimeout + downloadSetupTime + defaultTestDuration
	// LatencyOnly 最慢路径:少了下载两段，多了整段 Cloudflare 采样。
	latencyOnly := singBoxCheckTimeout + coreReadyTimeout + egressProbeTimeout + cfLatencyTotalTimeout

	for name, phases := range map[string]time.Duration{"下载测速": download, "仅测延迟": latencyOnly} {
		if phases > runExecutionBudget {
			t.Fatalf("%s 各阶段超时合计 %s 超过执行预算 %s", name, phases, runExecutionBudget)
		}
		if runExecutionBudget-phases < runPhaseMargin {
			t.Fatalf("%s 各阶段合计 %s 之后余量不足 %s(预算 %s)", name, phases, runPhaseMargin, runExecutionBudget)
		}
	}

	// 单次采样超时 × 采样数不得突破整段采样预算，否则 LatencyOnly 会绕过上面那笔加法。
	if cfLatencySampleTimeout*cfLatencySamples > cfLatencyTotalTimeout {
		t.Fatalf("Cloudflare 采样上限 %s × %d 超过整段预算 %s",
			cfLatencySampleTimeout, cfLatencySamples, cfLatencyTotalTimeout)
	}
}

// 退避重置改成「连接活过阈值才重置」之后，重连间隔本身要带抖动:主控重启会让大量家用
// 测速端同时掉线，固定间隔会让它们此后一直成批同步冲击主控。
func TestReconnectDelay落在半个退避到一个退避之间(t *testing.T) {
	for _, backoff := range []time.Duration{0, minReconnectBackoff, 8 * time.Second, maxReconnectBackoff} {
		want := backoff
		if want < minReconnectBackoff {
			want = minReconnectBackoff
		}
		seen := make(map[time.Duration]struct{})
		for range 200 {
			delay := reconnectDelay(backoff)
			if delay < want/2 || delay >= want {
				t.Fatalf("退避 %s 的重连间隔 %s 越界 [%s, %s)", backoff, delay, want/2, want)
			}
			seen[delay] = struct{}{}
		}
		if len(seen) < 2 {
			t.Fatalf("退避 %s 的重连间隔没有抖动: %v", backoff, seen)
		}
	}
}
