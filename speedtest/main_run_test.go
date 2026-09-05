package main

import "testing"

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
	dispatchRunJob(wsMsg{JobID: "busy-job"}, func(msg wsMsg) error {
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
