package main

import (
	"math"
	"reflect"
	"testing"
)

// clampSpeedTestParams 是家用测速端的 OOM 闸门:峰值内存约等于 BufSize×Threads，
// 而这两个值都由主控在任务里下发。此前该函数覆盖率为 0%，等于这条闸门没有任何回归保护。
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
		{"峰值内存超标时缩小 buf", maxBufSize, maxSpeedThreads, maxSpeedTotalMem / maxSpeedThreads, maxSpeedThreads},
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
			if peak := int64(bufSize) * int64(threads); peak > maxSpeedTotalMem {
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
