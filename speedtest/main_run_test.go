package main

import (
	"context"
	"testing"
)

func TestDispatchRunJob忙时立即失败(t *testing.T) {
	for range runConcurrency {
		runJobSlots <- struct{}{}
	}
	defer func() {
		for range runConcurrency {
			<-runJobSlots
		}
	}()

	var got wsMsg
	dispatchRunJob(context.Background(), wsMsg{JobID: "busy-job"}, func(msg wsMsg) error {
		got = msg
		return nil
	})

	if got.Type != "result" || got.JobID != "busy-job" {
		t.Fatalf("忙状态回包身份错误: %#v", got)
	}
	if got.Status != "failed" || got.Error != "测速端忙" {
		t.Fatalf("忙状态回包内容错误: %#v", got)
	}
}

func TestDispatchRunJob已断线不接纳任务(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	dispatchRunJob(ctx, wsMsg{JobID: "canceled-job"}, func(wsMsg) error {
		t.Error("已取消连接不应派发任务或回传结果")
		return nil
	})
	if got := len(runJobSlots); got != 0 {
		t.Fatalf("已取消连接仍占用 %d 个测速任务槽", got)
	}
}
