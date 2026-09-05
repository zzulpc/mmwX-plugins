package main

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
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

// 始终保留前序任务的执行权，避免把“手动解锁后才返回”误判成支持排队取消。
func occupyRunExecution(t *testing.T) func() {
	t.Helper()
	select {
	case runExecutionSlot <- struct{}{}:
	default:
		t.Fatal("测试开始时仍有任务占用执行权")
	}
	var once sync.Once
	release := func() { once.Do(func() { <-runExecutionSlot }) }
	t.Cleanup(release)
	return release
}

func TestDispatchRunJob排队超时释放任务槽(t *testing.T) {
	occupyRunExecution(t)
	results := make(chan wsMsg, 1)
	// 无效配置只能在进入执行后报解析错误；排队超时应在解析之前结束。
	dispatchRunJobWithBudgets(context.Background(), wsMsg{JobID: "queue-timeout", ClashConfig: "{"}, func(msg wsMsg) error {
		results <- msg
		return nil
	}, 30*time.Millisecond, time.Second)
	select {
	case got := <-results:
		if got.JobID != "queue-timeout" || got.Status != "failed" || !strings.Contains(got.Error, "等待执行超时") {
			t.Fatalf("排队超时未正确回报失败: %#v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("排队到期后仍在等待前序任务释放执行权")
	}
	waitForSlotCount(t, runJobSlots, 0, time.Second, "测速任务槽")
	if len(runExecutionSlot) != 1 {
		t.Fatal("排队失败释放了前序任务的执行权")
	}
}

func TestDispatchRunJob排队时断线立即释放任务槽(t *testing.T) {
	occupyRunExecution(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	results := make(chan wsMsg, 1)
	dispatchRunJobWithBudgets(ctx, wsMsg{JobID: "queue-canceled", ClashConfig: "{"}, func(msg wsMsg) error {
		results <- msg
		return nil
	}, time.Hour, time.Hour)
	cancel()
	select {
	case got := <-results:
		if got.Status != "failed" || !strings.Contains(got.Error, context.Canceled.Error()) {
			t.Fatalf("排队断线未保留取消原因: %#v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("连接取消后仍在等待前序任务")
	}
	waitForSlotCount(t, runJobSlots, 0, time.Second, "测速任务槽")
}

func TestBeginRun排队不消耗执行预算(t *testing.T) {
	release := occupyRunExecution(t)
	const executionBudget = time.Second
	type startedRun struct {
		ctx    context.Context
		finish func()
		err    error
	}
	started := make(chan startedRun, 1)
	go func() {
		ctx, finish, err := beginRun(context.Background(), time.Now().Add(2*time.Second), executionBudget)
		started <- startedRun{ctx: ctx, finish: finish, err: err}
	}()
	select {
	case got := <-started:
		if got.finish != nil {
			got.finish()
		}
		t.Fatalf("前序任务仍在执行时后续任务已返回: %v", got.err)
	case <-time.After(100 * time.Millisecond):
	}
	releasedAt := time.Now()
	release()
	select {
	case got := <-started:
		if got.err != nil {
			t.Fatal(got.err)
		}
		defer got.finish()
		deadline, ok := got.ctx.Deadline()
		if !ok || deadline.Before(releasedAt.Add(executionBudget)) || got.ctx.Err() != nil {
			t.Fatalf("排队等待压缩或取消了执行预算: deadline=%s, err=%v", deadline, got.ctx.Err())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("执行权释放后任务仍未开始")
	}
}

func TestBeginRun过期任务不能取得空闲执行权(t *testing.T) {
	_, finish, err := beginRun(context.Background(), time.Now().Add(-time.Second), time.Second)
	if finish != nil {
		finish()
	}
	if !errors.Is(err, context.DeadlineExceeded) || len(runExecutionSlot) != 0 {
		t.Fatalf("过期任务被执行或遗留槽位: %v", err)
	}
}

func TestBeginRun执行仍继承父期限和取消(t *testing.T) {
	parent, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	ctx, finish, err := beginRun(parent, time.Now().Add(time.Minute), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	defer finish()
	parentDeadline, _ := parent.Deadline()
	deadline, _ := ctx.Deadline()
	if !deadline.Equal(parentDeadline) {
		t.Fatal("执行期限未服从父任务的更短期限")
	}
	cancel()
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatalf("父任务取消没有传到执行阶段: %v", ctx.Err())
	}
}

func TestRunJob回包前释放执行权(t *testing.T) {
	// 配置解析失败也必须先交还执行权，回包可能受主控写锁或网络延迟影响。
	runJob(context.Background(), wsMsg{JobID: "invalid-config", ClashConfig: "{"}, func(got wsMsg) error {
		if got.Status != "failed" || !strings.Contains(got.Error, "解析节点") {
			t.Fatalf("配置失败回报不符: %#v", got)
		}
		ctx, finish, err := beginRun(context.Background(), time.Now().Add(50*time.Millisecond), time.Second)
		if err != nil {
			t.Fatalf("回包时仍占用执行权: %v", err)
		}
		finish()
		if !errors.Is(ctx.Err(), context.Canceled) {
			t.Fatal("释放执行权未结束执行上下文")
		}
		return nil
	})
}
