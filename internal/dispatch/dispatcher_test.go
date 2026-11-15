package dispatch

import (
	"context"
	"errors"
	"testing"

	"github.com/oekit/logx"

	"github.com/yi-nology/argus/internal/plugin"
)

// ---- test helpers ----

type nopPlugin struct{ manifest plugin.AgentManifest }

func (p *nopPlugin) Manifest() plugin.AgentManifest { return p.manifest }
func (p *nopPlugin) Invoke(_ context.Context, _ plugin.Task) (plugin.Result, error) {
	return plugin.Result{}, nil
}

type failPlugin struct{ manifest plugin.AgentManifest }

func (p *failPlugin) Manifest() plugin.AgentManifest { return p.manifest }
func (p *failPlugin) Invoke(_ context.Context, _ plugin.Task) (plugin.Result, error) {
	return plugin.Result{}, errors.New("boom")
}

func reg(name string, skills []string, opts ...func(*Registration)) *Registration {
	r := &Registration{
		Manifest: plugin.AgentManifest{Name: name, Skills: skills},
		Plugin:   &nopPlugin{manifest: plugin.AgentManifest{Name: name, Skills: skills}},
		Enabled:  true,
	}
	for _, o := range opts {
		o(r)
	}
	return r
}

func withLock(r *Registration)     { r.LockRepoConfig = true }
func withDisabled(r *Registration) { r.Enabled = false }
func withCost(c string) func(*Registration) {
	return func(r *Registration) { r.Manifest.Cost = c }
}
func withTrigger(paths []string) func(*Registration) {
	return func(r *Registration) { r.Manifest.Triggers.Paths = paths }
}

// ---- Select tests ----

func TestSelect_Basic(t *testing.T) {
	d := NewDispatcher(NewRegistry(), logx.Nop())
	_ = d.reg.Register(reg("security", []string{"security"}))
	_ = d.reg.Register(reg("correctness", []string{"correctness"}))

	pr := &plugin.PRContext{Platform: "gitea"}
	sel := d.Select(pr, nil, []string{"security"}, nil)
	if len(sel) != 1 || sel[0].Reg.Manifest.Name != "security" {
		t.Fatalf("应只选中 security，got %v", selNames(sel))
	}
}

func TestSelect_NoSkillMatch(t *testing.T) {
	d := NewDispatcher(NewRegistry(), logx.Nop())
	_ = d.reg.Register(reg("security", []string{"security"}))

	pr := &plugin.PRContext{Platform: "gitea"}
	sel := d.Select(pr, nil, []string{"nonexistent"}, nil)
	if len(sel) != 0 {
		t.Fatalf("无技能匹配应为空，got %v", selNames(sel))
	}
}

func TestSelect_Disabled(t *testing.T) {
	d := NewDispatcher(NewRegistry(), logx.Nop())
	_ = d.reg.Register(reg("security", []string{"security"}, withDisabled))

	pr := &plugin.PRContext{Platform: "gitea"}
	sel := d.Select(pr, nil, []string{"security"}, nil)
	if len(sel) != 0 {
		t.Fatalf("disabled 插件不应入选，got %v", selNames(sel))
	}
}

func TestSelect_LockBypassesDisable(t *testing.T) {
	d := NewDispatcher(NewRegistry(), logx.Nop())
	_ = d.reg.Register(reg("security", []string{"security"}, withLock))

	pr := &plugin.PRContext{Platform: "gitea"}
	// lock 插件在 disableSet 中也应入选（Select 不看 disableSet 对 lock）
	rc := &plugin.RepoConfig{Disable: []string{"security"}}
	sel := d.Select(pr, nil, []string{"security"}, rc)
	if len(sel) != 1 {
		t.Fatalf("lock 插件应绕过 disable，got %v", selNames(sel))
	}
	if sel[0].Reason != "lock" {
		t.Fatalf("reason 应为 lock，got %q", sel[0].Reason)
	}
}

func TestSelect_RepoConfigUseWhitelist(t *testing.T) {
	d := NewDispatcher(NewRegistry(), logx.Nop())
	_ = d.reg.Register(reg("security", []string{"security"}))
	_ = d.reg.Register(reg("correctness", []string{"correctness"}))

	rc := &plugin.RepoConfig{Use: []string{"security"}}
	pr := &plugin.PRContext{Platform: "gitea"}
	sel := d.Select(pr, nil, []string{"security", "correctness"}, rc)
	if len(sel) != 1 || sel[0].Reg.Manifest.Name != "security" {
		t.Fatalf("use 白名单应只选 security，got %v", selNames(sel))
	}
}

func TestSelect_RepoConfigDisableBlacklist(t *testing.T) {
	d := NewDispatcher(NewRegistry(), logx.Nop())
	_ = d.reg.Register(reg("security", []string{"security"}))
	_ = d.reg.Register(reg("correctness", []string{"correctness"}))

	rc := &plugin.RepoConfig{Disable: []string{"correctness"}}
	pr := &plugin.PRContext{Platform: "gitea"}
	sel := d.Select(pr, nil, []string{"security", "correctness"}, rc)
	if len(sel) != 1 || sel[0].Reg.Manifest.Name != "security" {
		t.Fatalf("disable 应排除 correctness，got %v", selNames(sel))
	}
}

func TestSelect_TriggerMatch(t *testing.T) {
	d := NewDispatcher(NewRegistry(), logx.Nop())
	_ = d.reg.Register(reg("security", []string{"security"}, withTrigger([]string{"src/**"})))

	pr := &plugin.PRContext{Platform: "gitea"}
	// 不匹配 → 不选
	sel := d.Select(pr, []string{"README.md"}, []string{"security"}, nil)
	if len(sel) != 0 {
		t.Fatalf("trigger 不匹配应不选，got %v", selNames(sel))
	}
	// 匹配 → 选
	sel = d.Select(pr, []string{"src/main.go"}, []string{"security"}, nil)
	if len(sel) != 1 {
		t.Fatalf("trigger 匹配应选中，got %v", selNames(sel))
	}
}

func TestSelect_Dedup(t *testing.T) {
	d := NewDispatcher(NewRegistry(), logx.Nop())
	_ = d.reg.Register(reg("security", []string{"security"}, withLock))
	// routing 也引用 security → 应去重
	rc := &plugin.RepoConfig{Routing: []plugin.RoutingRule{{Paths: []string{"**"}, Use: []string{"security"}}}}

	pr := &plugin.PRContext{Platform: "gitea"}
	sel := d.Select(pr, []string{"a.go"}, []string{"security"}, rc)
	if len(sel) != 1 {
		t.Fatalf("去重后应只有 1 个 security，got %v", selNames(sel))
	}
	if sel[0].Reason != "lock" {
		t.Fatalf("应保留首个原因 lock，got %q", sel[0].Reason)
	}
}

func TestSelect_SquadAppendsMembers(t *testing.T) {
	registry := NewRegistry()
	registry.Register(reg("gpt", []string{"code-review"}))
	registry.Register(reg("claude", []string{"code-review"}))
	registry.Register(reg("kimi", []string{"code-review"}))
	d := NewDispatcher(registry, logx.Nop()).
		WithSquads([]plugin.SquadDecl{{
			Name: "sec", Paths: []string{"**/auth/**"}, Members: []string{"claude", "kimi"},
		}})

	pr := &plugin.PRContext{Platform: "gitea", Owner: "o", Repo: "r", Number: "1"}
	sel := d.Select(pr, []string{"pkg/auth/login.go"}, []string{"code-review"}, nil)

	names := map[string]string{}
	for _, s := range sel {
		names[s.Reg.Manifest.Name] = s.Reason
	}
	if names["gpt"] != "base" {
		t.Fatalf("gpt 应 base 入选: %+v", names)
	}
	// claude/kimi 已有 base 原因 → 去重保留；编队追加只对未入选成员生效
	if _, ok := names["claude"]; !ok {
		t.Fatal("claude 应入选")
	}
	if m := d.ActiveSquadMembers(pr, []string{"pkg/auth/x.go"}); len(m) != 2 {
		t.Fatalf("ActiveSquadMembers 应返回 2 成员: %v", m)
	}
	// 未命中触发条件：不追加
	if m := d.ActiveSquadMembers(pr, []string{"pkg/other/x.go"}); len(m) != 0 {
		t.Fatalf("未命中不应触发编队: %v", m)
	}
}

// 收尾批修复回归：已触发的 consensus 编队成员不进 fan-out（此前注释声称已排除、
// 代码漏了——同一专家盲审一次+共识再跑一次）；编队未触发时照常参与基础选择。
func TestSelect_ConsensusMembersExcludedFromFanout(t *testing.T) {
	d := NewDispatcher(NewRegistry(), logx.Nop())
	_ = d.reg.Register(reg("correctness", []string{"code-review"}))
	_ = d.reg.Register(reg("security", []string{"code-review"}))
	d.WithSquads([]plugin.SquadDecl{{
		Name: "core-consensus", Mode: plugin.SquadModeConsensus,
		Paths: []string{"internal/**"}, Members: []string{"correctness", "security"},
	}})
	pr := &plugin.PRContext{Platform: "gitea"}
	// 编队触发：成员全被排除
	if sel := d.Select(pr, []string{"internal/a.go"}, []string{"code-review"}, nil); len(sel) != 0 {
		t.Fatalf("consensus 成员不应进 fan-out，got %v", selNames(sel))
	}
	// 编队未触发：成员照常参选
	if sel := d.Select(pr, []string{"cmd/main.go"}, []string{"code-review"}, nil); len(sel) != 2 {
		t.Fatalf("编队未触发时成员应正常参选，got %v", selNames(sel))
	}
}

// ---- helpers ----

func selNames(sel []Selection) []string {
	names := make([]string, len(sel))
	for i, s := range sel {
		names[i] = s.Reg.Manifest.Name
	}
	return names
}
