package qa

import (
	"context"
	"strings"
	"testing"

	"github.com/oagentkit/tools/workcopy"

	"github.com/yi-nology/argus/internal/semsearch"
)

// WC/SDK 未装配时，语义工具应返回「工作副本不可用」而不是空指针 panic。
// 两个操作数分别覆盖：WC==nil 短路；WC 非 nil 但 SDK==nil（守卫在 Ensure 前返回，
// 所以零值 Pool 不会被解引用）。
func TestSemToolsNilWorkcopyNoPanic(t *testing.T) {
	cases := []struct {
		name string
		e    *Engine
	}{
		{"wc_nil", &Engine{SemSearch: &semsearch.Manager{}, SemSearchOn: true}},
		{"sdk_nil", &Engine{
			SemSearch: &semsearch.Manager{}, SemSearchOn: true, WC: &workcopy.Pool{},
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := searchSem(c.e, context.Background(), "github", "o", "r", "1", "q", 5); err == nil ||
				!strings.Contains(err.Error(), "工作副本不可用") {
				t.Fatalf("searchSem = %v, want 工作副本不可用", err)
			}
			if _, err := traceCallChain(c.e, context.Background(), "github", "o", "r", "1", "a", "b"); err == nil ||
				!strings.Contains(err.Error(), "工作副本不可用") {
				t.Fatalf("traceCallChain = %v, want 工作副本不可用", err)
			}
			if _, err := symbolContext(c.e, context.Background(), "github", "o", "r", "1", "Sym"); err == nil ||
				!strings.Contains(err.Error(), "工作副本不可用") {
				t.Fatalf("symbolContext = %v, want 工作副本不可用", err)
			}
		})
	}
}
