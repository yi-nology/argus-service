// service_console_options_test.go —— GET /console/options 聚合词表契约（v8.7.0）：
// 四件词表与单源一致、repos 行过滤双闸（租户谓词 + token 仓库约束）。
package service

import (
	"context"
	"net/http"
	"slices"
	"testing"

	argus "github.com/yi-nology/argus/biz/model/argus"
	"github.com/yi-nology/argus/internal/config"
	"github.com/yi-nology/argus/internal/storage"
	"github.com/yi-nology/argus/internal/tenant"
)

// TestConsoleOptionsVocab 词表三件与单源一致 + kinds 覆盖 kindIDLTable 全集且升序。
func TestConsoleOptionsVocab(t *testing.T) {
	st := openVisStore(t)
	s := &Service{Cfg: &config.Config{}, Store: st}
	data, code := s.ConsoleOptions(context.Background())
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	platforms, _ := data["platforms"].([]string)
	if !slices.Equal(platforms, config.SupportedPlatforms) {
		t.Fatalf("platforms = %v, want %v", platforms, config.SupportedPlatforms)
	}
	statuses, _ := data["statuses"].([]string)
	if !slices.Equal(statuses, storage.AllTaskStatuses()) {
		t.Fatalf("statuses = %v, want %v", statuses, storage.AllTaskStatuses())
	}
	kinds, _ := data["kinds"].([]argus.KindOption)
	if len(kinds) != len(kindIDLTable) {
		t.Fatalf("kinds 数 = %d, want %d（kindIDLTable 全集）", len(kinds), len(kindIDLTable))
	}
	names := make(map[string]bool, len(kinds))
	last := -1
	for _, k := range kinds {
		if int(k.Value) <= last {
			t.Fatalf("kinds 非升序：%d 后出现 %d", last, int(k.Value))
		}
		last = int(k.Value)
		names[k.Name] = true
	}
	for _, name := range kindIDLTable {
		if !names[name] {
			t.Fatalf("kinds 缺 %q", name)
		}
	}
}

// TestConsoleOptionsRepoRowFilter repos 双闸：跨租户名不透出 + 静态 token 约束外剔除。
func TestConsoleOptionsRepoRowFilter(t *testing.T) {
	st := openVisStore(t)
	s := &Service{Cfg: &config.Config{}, Store: st}
	ctx := tenant.WithoutScope(context.Background())
	for _, row := range []storage.TenantRepo{
		{TenantID: "", Platform: "gitea", Owner: "myorg", Repo: "app"},          // default 租户（空=Default）
		{TenantID: "default", Platform: "gitea", Owner: "myorg", Repo: "web"},   // default 租户（显式）
		{TenantID: "default", Platform: "gitea", Owner: "outside", Repo: "x"},   // 同租户但 token 约束外
		{TenantID: "team-b", Platform: "gitea", Owner: "other", Repo: "secret"}, // 跨租户
	} {
		if err := st.UpsertTenantRepo(ctx, row); err != nil {
			t.Fatal(err)
		}
	}
	caller := ContextWithTokenRepos(tenant.NewContext(context.Background(), ""), "gitea:myorg/*")
	data, code := s.ConsoleOptions(caller)
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	repos, _ := data["repos"].([]argus.RepoOption)
	if len(repos) != 2 {
		t.Fatalf("repos 数 = %d（%v），want 2（myorg/app + myorg/web）", len(repos), repos)
	}
	got := map[string]bool{}
	for _, r := range repos {
		got[r.Owner+"/"+r.Repo] = true
	}
	if !got["myorg/app"] || !got["myorg/web"] {
		t.Fatalf("缺约束内仓库：%v", got)
	}
	if got["outside/x"] || got["other/secret"] {
		t.Fatalf("约束外/跨租户仓库漏出：%v", got)
	}
}
