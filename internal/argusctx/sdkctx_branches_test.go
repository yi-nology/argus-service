// sdkctx_branches_test.go —— BranchManager 分支清单面（v8.8.0）：名称提取
// 归一（nil/空名/重复/排序）+ 2 分钟缓存命中不重复打平台。
package argusctx

import (
	"context"
	"slices"
	"sync/atomic"
	"testing"

	"github.com/yi-nology/go-git-platform/model"
	"github.com/yi-nology/go-git-platform/provider"
)

// branchesFake 只实现 ListBranches（计数）；未覆盖方法借嵌入接口 panic 兜底。
type branchesFake struct {
	provider.Provider
	n atomic.Int64
}

func (f *branchesFake) ListBranches(context.Context, string, string) ([]*model.PlatformBranch, error) {
	f.n.Add(1)
	return []*model.PlatformBranch{
		{Name: "main"},
		{Name: "release/1.2"},
		{Name: ""},
		nil,
		{Name: "main"},
		{Name: "dev"},
	}, nil
}

func TestBranchNamesNormalize(t *testing.T) {
	got := branchNames([]*model.PlatformBranch{
		nil, {Name: "b"}, {Name: ""}, {Name: "a"}, {Name: "b"}, {Name: "  "},
	})
	want := []string{"a", "b"}
	if !slices.Equal(got, want) {
		t.Fatalf("branchNames = %v, want %v", got, want)
	}
}

func TestListBranchesCache(t *testing.T) {
	f := &branchesFake{}
	s := NewSDKCtxStubbed(nopLogx{}, map[string]provider.Provider{"github": f})
	first, err := s.ListBranches(context.Background(), "github", "o", "r")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if want := []string{"dev", "main", "release/1.2"}; !slices.Equal(first, want) {
		t.Fatalf("branches = %v, want %v", first, want)
	}
	if f.n.Load() != 1 {
		t.Fatalf("首次应打 1 次平台, got %d", f.n.Load())
	}
	// 第二次走缓存
	if second, _ := s.ListBranches(context.Background(), "github", "o", "r"); !slices.Equal(second, first) {
		t.Fatalf("缓存读取不一致: %v", second)
	}
	if f.n.Load() != 1 {
		t.Fatalf("缓存命中不应再打平台, got %d", f.n.Load())
	}
}

func TestListBranchesUnassembled(t *testing.T) {
	s := NewSDKCtxStubbed(nopLogx{}, map[string]provider.Provider{})
	if _, err := s.ListBranches(context.Background(), "github", "o", "r"); err == nil {
		t.Fatal("未装配平台应返回错误")
	}
}
