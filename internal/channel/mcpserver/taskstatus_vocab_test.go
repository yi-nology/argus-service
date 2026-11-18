package mcpserver

import (
	"slices"
	"testing"

	"github.com/yi-nology/argus/internal/storage"
)

// 跨包词表一致性钉（mcpserver 零 internal 依赖铁律的镜像形态）：本包状态
// 词表与 owner（internal/storage 任务状态词表）逐值相等——storage 加新状态
// 这里没跟进当天红（word_tables 门禁的 mcpserver 侧）。
func TestTaskStatusVocabMatchesStorage(t *testing.T) {
	want := storage.AllTaskStatuses()
	got := slices.Clone(taskStatusChoices)
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Fatalf("mcpserver 状态词表与 storage 漂移: got=%v want=%v", got, want)
	}
}
