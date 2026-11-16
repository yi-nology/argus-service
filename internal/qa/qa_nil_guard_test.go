package qa

import (
	"context"
	"strings"
	"testing"

	"github.com/yi-nology/argus/internal/semsearch"
)

// WC/SDK 未装配时，语义工具应返回「工作副本不可用」而不是空指针 panic。
func TestSemToolsNilWorkcopyNoPanic(t *testing.T) {
	e := &Engine{SemSearch: &semsearch.Manager{}, SemSearchOn: true}
	if _, err := searchSem(e, context.Background(), "github", "o", "r", "1", "q", 5); err == nil ||
		!strings.Contains(err.Error(), "工作副本不可用") {
		t.Fatalf("searchSem = %v, want 工作副本不可用", err)
	}
	if _, err := traceCallChain(e, context.Background(), "github", "o", "r", "1", "a", "b"); err == nil ||
		!strings.Contains(err.Error(), "工作副本不可用") {
		t.Fatalf("traceCallChain = %v, want 工作副本不可用", err)
	}
}