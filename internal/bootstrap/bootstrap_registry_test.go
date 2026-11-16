package bootstrap

// BuildRegistry/插件清单装载的行为测试：工厂分支的启用/停用语义、配置错误
// fail-fast、清单缺失回退与严格解析，都是启动期信任边界（§3.4/§8）的一部分。

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/oekit/logx"

	"github.com/yi-nology/argus/internal/config"
	"github.com/yi-nology/argus/internal/dispatch"
	"github.com/yi-nology/argus/internal/plugin"
)

func boolPtr(b bool) *bool { return &b }

func writePluginsYAML(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "plugins.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestBuildRegistryDisablesUnavailableVariants 钉死各类「声明了但装不起来」的
// 插件都注册为 Enabled=false 的可观测记录（unavailablePlugin），而不是启动失败
// 或静默消失——/agents 要能看到它们与原因。
func TestBuildRegistryDisablesUnavailableVariants(t *testing.T) {
	pf := &plugin.PluginsFile{Agents: []plugin.AgentDecl{
		{Name: "cli-no-cmd", Kind: "cli", Enabled: boolPtr(true)},
		{Name: "cli-delegate", Kind: "cli", Cmd: []string{"/bin/echo"}, Mode: "delegate"},
		{Name: "cli-missing-bin", Kind: "cli", Cmd: []string{"argus-d2-definitely-missing-binary"}},
		{Name: "remote-bad-proto", Kind: "remote", Protocol: "http", Endpoint: "http://127.0.0.1:1"},
		{Name: "remote-no-endpoint", Kind: "remote", Protocol: "mcp"},
		{Name: "remote-auth-unset", Kind: "remote", Protocol: "a2a", Endpoint: "http://127.0.0.1:1",
			AuthTokenEnv: "ARGUS_D2_DEFINITELY_UNSET_TOKEN_ENV"},
		{Name: "acpx-no-agent", Kind: "acpx"},
		{Name: "acpx-unknown-agent", Kind: "acpx", Agent: "argus-d2-unknown-agent"},
		// 无 LLM 的 builtin 仍应注册（运行期不可用由配置层告警），且 reviewN 超限钳制。
		{Name: "builtin-reviewer", Kind: "builtin", Skills: []string{"security"},
			Timeout: "45s", ReviewN: 99, Needs: []string{"diff", "bogus-need"}},
		{Name: "builtin-disabled", Kind: "builtin", Enabled: boolPtr(false)},
		// 声明非法（缺 name/kind）直接跳过，不注册。
		{Name: "", Kind: "cli"},
		{Name: "no-kind"},
	}}
	d := &Deps{
		Cfg:     &config.Config{LLM: config.LLMConfig{Model: "main-model"}},
		Log:     logx.Nop(),
		Plugins: pf,
	}

	reg, refs, err := BuildRegistry(d)
	if err != nil {
		t.Fatalf("BuildRegistry: %v", err)
	}
	if len(refs) != 0 {
		t.Fatalf("无 compliance 插件时 skillRefs 应为空: %v", refs)
	}
	if len(reg.Snapshot()) != 10 {
		t.Fatalf("注册数 = %d, want 10（非法声明跳过）", len(reg.Snapshot()))
	}

	disabled := []string{
		"cli-no-cmd", "cli-delegate", "cli-missing-bin",
		"remote-bad-proto", "remote-no-endpoint", "remote-auth-unset",
		"acpx-no-agent", "acpx-unknown-agent",
	}
	for _, name := range disabled {
		got, ok := reg.Get(name)
		if !ok {
			t.Fatalf("%s 未注册（应注册为不可用记录）", name)
		}
		if got.Enabled {
			t.Errorf("%s Enabled = true, want false", name)
		}
		if _, err := got.Plugin.Invoke(context.Background(), plugin.Task{}); err == nil {
			t.Errorf("%s Invoke 应报不可用", name)
		}
	}
	if got, ok := reg.Get("builtin-disabled"); !ok || got.Enabled {
		t.Fatalf("builtin-disabled 应注册且 Enabled=false: %+v", got)
	}

	reviewer, ok := reg.Get("builtin-reviewer")
	if !ok {
		t.Fatal("builtin-reviewer 未注册")
	}
	if !reviewer.Enabled {
		t.Fatal("builtin-reviewer 应启用")
	}
	if reviewer.Manifest.Timeout != 45*time.Second {
		t.Errorf("timeout = %v, want 45s", reviewer.Manifest.Timeout)
	}
	if reviewer.Manifest.ReviewN != dispatch.MaxReviewN {
		t.Errorf("ReviewN = %d, want 钳制到 %d", reviewer.Manifest.ReviewN, dispatch.MaxReviewN)
	}
	if !reviewer.Manifest.Needs.Diff || reviewer.Manifest.Needs.WorkingCopy {
		t.Errorf("Needs 解析错: %+v", reviewer.Manifest.Needs)
	}
	if len(reviewer.Manifest.Skills) != 1 || reviewer.Manifest.Skills[0] != "security" {
		t.Errorf("skills 透传错: %v", reviewer.Manifest.Skills)
	}
}

// TestBuildRegistryNegativeReviewNClamped 负数 reviewN 视为关闭（0），
// 不能原样进入 manifest（下游审阅 N 次语义会被负值污染）。
func TestBuildRegistryNegativeReviewNClamped(t *testing.T) {
	pf := &plugin.PluginsFile{Agents: []plugin.AgentDecl{
		{Name: "cli1", Kind: "cli", Cmd: []string{"/bin/echo"}, ReviewN: -3},
	}}
	reg, _, err := BuildRegistry(&Deps{
		Cfg: &config.Config{}, Log: logx.Nop(), Plugins: pf,
	})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := reg.Get("cli1")
	if got == nil || got.Manifest.ReviewN != 0 {
		t.Fatalf("ReviewN = %+v, want 0", got)
	}
}

func TestBuildRegistryInvalidTimeoutFailsFast(t *testing.T) {
	pf := &plugin.PluginsFile{Agents: []plugin.AgentDecl{
		{Name: "bad-timeout", Kind: "cli", Cmd: []string{"/bin/echo"}, Timeout: "not-a-duration"},
	}}
	_, _, err := BuildRegistry(&Deps{Cfg: &config.Config{}, Log: logx.Nop(), Plugins: pf})
	if err == nil || !strings.Contains(err.Error(), "timeout 非法") {
		t.Fatalf("want timeout 非法, got %v", err)
	}
}

func TestBuildRegistryUnknownKindFailsFast(t *testing.T) {
	pf := &plugin.PluginsFile{Agents: []plugin.AgentDecl{
		{Name: "weird", Kind: "quantum"},
	}}
	_, _, err := BuildRegistry(&Deps{Cfg: &config.Config{}, Log: logx.Nop(), Plugins: pf})
	if err == nil || !strings.Contains(err.Error(), "kind 非法") {
		t.Fatalf("want kind 非法, got %v", err)
	}
}

// TestBuildRegistryComplianceRequiresFactory requirement-compliance 的延迟绑定
// 依赖 workflow factory；缺 factory 必须启动期报错，不能注册后运行期 panic。
func TestBuildRegistryComplianceRequiresFactory(t *testing.T) {
	pf := &plugin.PluginsFile{Agents: []plugin.AgentDecl{
		{Name: "requirement-compliance", Kind: "builtin",
			Skills: []string{plugin.SkillRequirementCompliance}},
	}}
	_, _, err := BuildRegistry(&Deps{Cfg: &config.Config{}, Log: logx.Nop(), Plugins: pf})
	if err == nil || !strings.Contains(err.Error(), "workflow factory") {
		t.Fatalf("want 缺少 workflow factory, got %v", err)
	}
}

func TestBuildRegistryDuplicateFailsFast(t *testing.T) {
	pf := &plugin.PluginsFile{Agents: []plugin.AgentDecl{
		{Name: "dup", Kind: "cli", Cmd: []string{"/bin/echo"}},
		{Name: "dup", Kind: "cli", Cmd: []string{"/bin/echo"}},
	}}
	_, _, err := BuildRegistry(&Deps{Cfg: &config.Config{}, Log: logx.Nop(), Plugins: pf})
	if err == nil || !strings.Contains(err.Error(), "重复注册") {
		t.Fatalf("want 重复注册, got %v", err)
	}
}

// TestBuildRegistryLoadsPluginsFileWhenDepsPluginsNil Deps.Plugins 为空时
// BuildRegistry 自行装载（独立调用方语义），合法清单 → 正常注册。
func TestBuildRegistryLoadsPluginsFileWhenDepsPluginsNil(t *testing.T) {
	path := writePluginsYAML(t, `
agents:
  - name: echo-cli
    kind: cli
    cmd: ["/bin/echo"]
    timeout: 30s
`)
	reg, _, err := BuildRegistry(&Deps{
		Cfg: &config.Config{PluginsFile: path}, Log: logx.Nop(),
	})
	if err != nil {
		t.Fatalf("BuildRegistry: %v", err)
	}
	got, ok := reg.Get("echo-cli")
	if !ok || !got.Enabled {
		t.Fatalf("echo-cli 未按清单启用: %+v", got)
	}
}

func TestLoadPluginsFileMissingFallsBackToBuiltinDefaults(t *testing.T) {
	cfg := &config.Config{PluginsFile: filepath.Join(t.TempDir(), "absent.yaml")}
	pf, err := loadPluginsFile(cfg, logx.Nop())
	if err != nil {
		t.Fatalf("缺文件应回退内置默认: %v", err)
	}
	if len(pf.Agents) != 6 {
		t.Fatalf("默认插件数 = %d, want 6", len(pf.Agents))
	}
	names := map[string]plugin.AgentDecl{}
	for _, a := range pf.Agents {
		names[a.Name] = a
	}
	compliance, ok := names["requirement-compliance"]
	if !ok || !compliance.LockRepoConfig {
		t.Fatal("默认清单必须包含锁定仓库配置的 requirement-compliance")
	}
	visual, ok := names["frontend-visual"]
	if !ok || visual.Enabled == nil || *visual.Enabled {
		t.Fatal("默认清单 frontend-visual 应显式 disabled")
	}
	security, ok := names["security"]
	if !ok || security.Timeout != "600s" || !security.LockRepoConfig {
		t.Fatalf("默认 security 声明漂移: %+v", security)
	}
}

func TestLoadPluginsFileParseErrorFailsFast(t *testing.T) {
	path := writePluginsYAML(t, "agents: [\n")
	_, err := loadPluginsFile(&config.Config{PluginsFile: path}, logx.Nop())
	if err == nil || !strings.Contains(err.Error(), "插件清单解析失败") {
		t.Fatalf("坏 YAML 应 fail-fast, got %v", err)
	}
}

func TestLoadPluginsFileEmptyAgentsKept(t *testing.T) {
	path := writePluginsYAML(t, "agents: []\n")
	pf, err := loadPluginsFile(&config.Config{PluginsFile: path}, logx.Nop())
	if err != nil {
		t.Fatalf("空清单不是解析错误: %v", err)
	}
	if len(pf.Agents) != 0 {
		t.Fatalf("空清单应保持为空（不静默补默认）: %d", len(pf.Agents))
	}
}
