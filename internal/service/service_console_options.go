// service_console_options.go —— GET /console/options 聚合词表（v8.7.0 控制台
// 可用性迭代）：前端坐标选择器/类型下拉的单源数据面。
//
// 组成四件，全读词表单源，零新表：
//   - platforms：config.SupportedPlatforms（SDK 注册后端全集，防漂移测试钉）
//   - kinds：API 可建类型全集（kindIDLTable 派生，IDL 数值码 wire 直用）
//   - statuses：storage.AllTaskStatuses()（word_tables 门禁单源）
//   - repos：Store.ListTenantRepos 登记面 + 行过滤（租户谓词 + AuthorizeRowFilter，
//     同 statsOverview 先例——tenant_repos 是全租户注册表，store 方法刻意
//     WithoutScope，租户维度必须在 service 层收口）
package service

import (
	"context"
	"net/http"
	"slices"

	argus "github.com/yi-nology/argus/biz/model/argus"
	"github.com/yi-nology/argus/internal/config"
	"github.com/yi-nology/argus/internal/storage"
	"github.com/yi-nology/argus/internal/tenant"
)

// ConsoleOptions 控制台选项聚合（read 档；repos 行过滤同 statsOverview）。
func (s *Service) ConsoleOptions(ctx context.Context) (map[string]any, int) {
	rows, err := s.Store.ListTenantRepos(ctx)
	if err != nil {
		s.log().Error("argus.console_options.repos_failed", "error", err.Error())
		return nil, http.StatusInternalServerError
	}
	caller := tenant.FromContext(ctx)
	filter := s.AuthorizeRowFilter(ctx)
	repos := make([]argus.RepoOption, 0, len(rows))
	for _, r := range rows {
		if tenant.Normalize(r.TenantID) != caller {
			continue // 跨租户仓库名不透出（tenant_repos 是全租户表，store 刻意 WithoutScope）
		}
		if filter != nil && !filter(r.Platform, r.Owner, r.Repo) {
			continue // 静态 token 仓库约束，同 statsOverview 行过滤
		}
		repos = append(repos, argus.RepoOption{Platform: r.Platform, Owner: r.Owner, Repo: r.Repo})
	}
	return map[string]any{
		"platforms": config.SupportedPlatforms,
		"kinds":     consoleKindOptions(),
		"statuses":  storage.AllTaskStatuses(),
		"repos":     repos,
	}, http.StatusOK
}

// consoleKindOptions 任务类型选项（kindIDLTable 派生，按 IDL 数值升序）。
func consoleKindOptions() []argus.KindOption {
	vals := make([]int, 0, len(kindIDLTable))
	byVal := make(map[int]string, len(kindIDLTable))
	for k, name := range kindIDLTable {
		vals = append(vals, int(k))
		byVal[int(k)] = name
	}
	slices.Sort(vals)
	out := make([]argus.KindOption, 0, len(vals))
	for _, v := range vals {
		out = append(out, argus.KindOption{Value: argus.TaskKind(v), Name: byVal[v]})
	}
	return out
}
