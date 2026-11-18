package storage

import (
	"slices"
	"testing"
)

func TestTaskStatusVocab(t *testing.T) {
	// 判定谓词：五值全过，域外拒。
	for _, s := range []string{StatusPending, StatusRunning, StatusDone, StatusFailed, StatusStale} {
		if !IsKnownTaskStatus(s) {
			t.Fatalf("%q 应判定已知", s)
		}
	}
	for _, s := range []string{"", "PENDING", "cancelled", "open"} {
		if IsKnownTaskStatus(s) {
			t.Fatalf("%q 不应判定已知（大小写/空白不规整属输入面，判定面精确）", s)
		}
	}
	// 枚举出口：与词表逐值相等且为副本（改返回值不影响判定面）。
	all := AllTaskStatuses()
	if len(all) != 5 {
		t.Fatalf("词表应 5 值: %v", all)
	}
	all[0] = "mutated"
	if IsKnownTaskStatus("mutated") {
		t.Fatal("枚举返回必须是副本")
	}
	// 派生组：在飞=过程态两值。
	inFlight := InFlightTaskStatuses()
	slices.Sort(inFlight)
	if !slices.Equal(inFlight, []string{StatusPending, StatusRunning}) {
		t.Fatalf("在飞组应 pending+running: %v", inFlight)
	}
	// 分类表与词表同键集（backing 单源不漂移）。
	if len(taskStatusAll) != 5 {
		t.Fatalf("backing 键集应 5: %v", taskStatusAll)
	}
}
