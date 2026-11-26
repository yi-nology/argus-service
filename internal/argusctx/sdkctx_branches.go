// sdkctx_branches.go —— BranchManager 分支清单面（v8.8.0 控制台分支下拉）：
// 任仓分支名清单 + TTL 缓存。默认分支仍走 ResolveBranch（GetRepo 面）。
package argusctx

import (
	"context"
	"slices"
	"strings"
	"time"

	datacache "github.com/oekit/data/cache"

	"github.com/yi-nology/go-git-platform/model"
	"github.com/yi-nology/go-git-platform/provider"

	"github.com/yi-nology/argus/internal/tenant"
)

// ListBranches 仓库分支名清单（去重升序；租户感知凭证 + 2 分钟缓存）。
// 平台不支持 BranchManager 时返回 unsupportedErr（调用方映射 400）。
func (s *SDKCtx) ListBranches(ctx context.Context, platform, owner, repo string) ([]string, error) {
	key := tenant.Key(tenant.FromContext(ctx), strings.ToLower(platform)+"/"+owner+"/"+repo)
	if v, err := s.branchListCache().Get(ctx, key); err == nil && v != nil {
		return *v, nil
	}
	list, err := callIface(s, ctx, platform, "BranchManager",
		func(ctx context.Context, bm provider.BranchManager) ([]*model.PlatformBranch, error) {
			return bm.ListBranches(ctx, owner, repo)
		})
	if err != nil {
		return nil, err
	}
	names := branchNames(list)
	_ = s.branchListCache().Set(ctx, key, &names)
	return names, nil
}

// branchNames 分支名提取：丢弃 nil/空名，去重后升序（确定性输出）。
func branchNames(list []*model.PlatformBranch) []string {
	seen := make(map[string]struct{}, len(list))
	names := make([]string, 0, len(list))
	for _, b := range list {
		if b == nil || strings.TrimSpace(b.Name) == "" {
			continue
		}
		if _, ok := seen[b.Name]; ok {
			continue
		}
		seen[b.Name] = struct{}{}
		names = append(names, b.Name)
	}
	slices.Sort(names)
	return names
}

// branchListCache 分支清单缓存懒建（TTL 2 分钟；仓库分支变动不频繁）。
func (s *SDKCtx) branchListCache() *datacache.LocalCache[[]string] {
	s.branchListMu.Lock()
	defer s.branchListMu.Unlock()
	if s.branchListCch == nil {
		s.branchListCch = datacache.NewLocalCache(datacache.LocalConfig[[]string]{
			TTL:     2 * time.Minute,
			MaxSize: 200,
		})
	}
	return s.branchListCch
}
