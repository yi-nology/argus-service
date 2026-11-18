// vocab_task_status_test.go —— 任务状态词表三件套补测（词表批 c5b281c 新增
// 代码的覆盖率回填：ratchet 基线 0.684，词表纯函数无测跌至 0.677）。
package storage

import (
	"slices"
	"testing"
)

// 词表全集：五态齐备 + 排序 + 新切片（调用方改返回不影响词表）。
func TestAllTaskStatuses(t *testing.T) {
	got := AllTaskStatuses()
	want := []string{StatusDone, StatusFailed, StatusPending, StatusRunning, StatusStale}
	if !slices.Equal(got, want) {
		t.Fatalf("AllTaskStatuses = %v, want %v", got, want)
	}
	got[0] = "mutated" // 新切片语义：词表不受调用方改写影响
	if AllTaskStatuses()[0] != StatusDone {
		t.Fatal("AllTaskStatuses 必须返回新切片（backing 词表被调用方污染）")
	}
}

// 谓词：词形内真词形外假 + 大小写敏感。
func TestIsKnownTaskStatus(t *testing.T) {
	for _, s := range AllTaskStatuses() {
		if !IsKnownTaskStatus(s) {
			t.Errorf("IsKnownTaskStatus(%q) = false, want true", s)
		}
	}
	for _, s := range []string{"", "DONE", "Done", "cancelling", "queued"} {
		if IsKnownTaskStatus(s) {
			t.Errorf("IsKnownTaskStatus(%q) = true, want false", s)
		}
	}
}

// 在飞派生组：过程态两枚 + 排序 + 成员分类自洽。
func TestInFlightTaskStatuses(t *testing.T) {
	got := InFlightTaskStatuses()
	want := []string{StatusPending, StatusRunning}
	if !slices.Equal(got, want) {
		t.Fatalf("InFlightTaskStatuses = %v, want %v", got, want)
	}
	for _, s := range got {
		if taskStatusAll[s] != TaskStatusOpen {
			t.Errorf("%s 在飞组但分类=%v", s, taskStatusAll[s])
		}
	}
}

// 分类表完整性：在飞组成员资格与分类一致 + 五态齐备。
func TestTaskStatusAllPartition(t *testing.T) {
	inflight := InFlightTaskStatuses()
	for s, c := range taskStatusAll {
		if (c == TaskStatusOpen) != slices.Contains(inflight, s) {
			t.Errorf("%s 分类=%v 与在飞资格不一致", s, c)
		}
	}
	if len(taskStatusAll) != 5 {
		t.Fatalf("词表登记 %d 态, want 5", len(taskStatusAll))
	}
}
