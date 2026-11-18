// knowledgefilter_test.go —— 租户过滤装饰器行为钉（#84/#92）。

package knowledgefilter

import (
	"context"
	"testing"

	"github.com/cloudwego/eino/components/tool"

	"github.com/oagentkit/rag"

	"github.com/yi-nology/argus/internal/tenant"
)

type fakeStore struct {
	chunks []rag.Chunk
}

func (f *fakeStore) Retrieve(_ context.Context, _ string, topK int, _ rag.Filter) ([]rag.Chunk, error) {
	out := f.chunks
	if len(out) > topK {
		out = out[:topK]
	}
	return out, nil
}

func (f *fakeStore) AsTool() tool.BaseTool { return nil }

func TestWrapFiltersByTenant(t *testing.T) {
	inner := &fakeStore{chunks: []rag.Chunk{
		{Content: "租户A的误报模式", Metadata: map[string]string{"file": "t-tenant-a/feedback/x"}},
		{Content: "租户B的误报模式", Metadata: map[string]string{"file": "t-tenant-b/feedback/y"}},
		{Content: "legacy全局规范", Metadata: map[string]string{"file": "spec/review.md"}},
	}}
	wrapped := Wrap(inner)
	if wrapped == nil {
		t.Fatal("Wrap 不得返回 nil")
	}
	ctxA := tenant.NewContext(context.Background(), "tenant-a")
	got, err := wrapped.Retrieve(ctxA, "q", 10, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("tenant-a 应见 2 条（本租户+default legacy），got %d", len(got))
	}
	for _, c := range got {
		if c.Metadata["file"] == "t-tenant-b/feedback/y" {
			t.Fatalf("串租户: %v", c.Metadata)
		}
	}
	// default 租户：只见 legacy，不见任何 t- 前缀键。
	gotDef, err := wrapped.Retrieve(context.Background(), "q", 10, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(gotDef) != 1 || gotDef[0].Metadata["file"] != "spec/review.md" {
		t.Fatalf("default 租户应只见 legacy 键: %+v", gotDef)
	}
	// Noop 透传。
	if got := Wrap(&rag.Noop{}); got == nil {
		t.Fatal("Noop Wrap 应透传非 nil")
	}
}

func TestChunkOwnedBy(t *testing.T) {
	cases := []struct {
		file, tid string
		want      bool
	}{
		{"t-a/dream/x", "a", true},
		{"t-a/dream/x", "b", false},
		{"dream/x", "default", true},
		{"dream/x", "a", true},  // legacy 共享基线语料全租户可见
		{"t-/weird", "a", true}, // 畸形前缀归共享
	}
	for _, c := range cases {
		got := chunkOwnedBy(rag.Chunk{Metadata: map[string]string{"file": c.file}}, c.tid)
		if got != c.want {
			t.Errorf("chunkOwnedBy(%q, %q) = %v, want %v", c.file, c.tid, got, c.want)
		}
	}
}
