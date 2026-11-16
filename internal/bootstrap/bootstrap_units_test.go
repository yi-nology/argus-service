package bootstrap

// 装配辅助函数测试（纯/可注入面）：模型+知识栈、插件 registry 延迟绑定、
// 编排/runner 构造、语义检索与记忆内核、webhook ensure 前置门与配置解析。
// 不装配存储/HTTP 全对象图。

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/oagentkit/llm"
	"github.com/oekit/logx"
	"github.com/yi-nology/goagent-kernel/pii"

	"github.com/yi-nology/argus/internal/config"
	"github.com/yi-nology/argus/internal/dispatch"
	"github.com/yi-nology/argus/internal/plugin"
	"github.com/yi-nology/argus/internal/poller"
	"github.com/yi-nology/argus/internal/runner"
	"github.com/yi-nology/argus/internal/storage"
)

func TestBuildModelKnowledgeStackEmptyAndNotify(t *testing.T) {
	sys := &System{}
	if err := buildModelKnowledgeStack(context.Background(), &config.Config{}, logx.Nop(), sys); err != nil {
		t.Fatalf("空配置不应失败: %v", err)
	}
	if sys.Models == nil || sys.Models.React != nil {
		t.Fatalf("无 LLM 配置应得到空模型栈: %+v", sys.Models)
	}
	if sys.Bus == nil || sys.EventSeen == nil {
		t.Fatal("进度总线/去重环必须装配")
	}
	if sys.Notify != nil {
		t.Fatal("未配置 octopus 时 Notify 应为 nil")
	}
	if sys.Knowledge == nil {
		t.Fatal("知识服务必须装配（无配置时 Noop）")
	}

	sys2 := &System{}
	cfg := &config.Config{OctopusURL: "http://octopus.test", OctopusChannelID: 42,
		OctopusUser: "u", OctopusToken: "tok"}
	if err := buildModelKnowledgeStack(context.Background(), cfg, logx.Nop(), sys2); err != nil {
		t.Fatal(err)
	}
	if sys2.Notify == nil || sys2.Notify.BaseURL != "http://octopus.test" ||
		sys2.Notify.ChannelID != 42 || sys2.Notify.Token != "tok" {
		t.Fatalf("Notify 接线错: %+v", sys2.Notify)
	}
}

// TestBuildPluginRegistryWiring 延迟绑定装配入口：registry 建好、skillRefs 透传，
// 不触发 runner 构造（nil runnerRef 的闭包分支由 BuildRegistry 侧契约保证）。
func TestBuildPluginRegistryWiring(t *testing.T) {
	pf := &plugin.PluginsFile{Agents: []plugin.AgentDecl{
		{Name: "echo-cli", Kind: "cli", Cmd: []string{"/bin/echo"}},
	}}
	var runnerRef *runner.Runner
	reg, refs, err := buildPluginRegistry(&config.Config{}, logx.Nop(), &System{}, pf,
		&Models{}, &runnerRef)
	if err != nil {
		t.Fatalf("buildPluginRegistry: %v", err)
	}
	if reg == nil || len(refs) != 0 {
		t.Fatalf("registry/refs 错: %v %v", reg, refs)
	}
	if _, ok := reg.Get("echo-cli"); !ok {
		t.Fatal("echo-cli 未注册")
	}
}

func TestBuildOrchestrationWiresComponents(t *testing.T) {
	sys := &System{}
	auditLog := buildOrchestration(&config.Config{}, logx.Nop(), nil, nil,
		dispatch.NewRegistry(), nil, "v9", sys)
	if auditLog == nil {
		t.Fatal("审计 logger 必须装配")
	}
	if sys.Dispatcher == nil || sys.Merger == nil || sys.Poster == nil {
		t.Fatal("dispatcher/merger/poster 必须装配")
	}
	if sys.Poster.Version != "v9" {
		t.Errorf("poster version = %q, want v9", sys.Poster.Version)
	}

	// 学习环 + 审计外送分支：构造不发网络（仅结构/包装）。
	sys2 := &System{}
	cfg := &config.Config{RegretLearning: true, AuditWebhookURL: "http://audit.test/hook",
		AuditWebhookToken: "t"}
	if got := buildOrchestration(cfg, logx.Nop(), nil, nil, dispatch.NewRegistry(), nil, "v", sys2); got == nil {
		t.Fatal("审计外送包装后 logger 不应为 nil")
	}
	if sys2.Merger == nil {
		t.Fatal("学习环开启时 merger 仍须装配")
	}
}

func TestBuildRunnerPassesThroughConfig(t *testing.T) {
	cfg := &config.Config{ReflectRounds: 2, ReflectEvidence: "strict", CompletenessRetry: true}
	cfg.LLM.Model = "main"
	models := &Models{FallbackName: "fb", StageLLMs: map[string]llm.Generator{}}
	sys := &System{}
	r := buildRunner(cfg, logx.Nop(), nil, nil, models, nil, []string{"ocr-grading"}, "v1", sys)
	if r == nil {
		t.Fatal("runner 未装配")
	}
	if r.ModelName != "main" || r.Version != "v1" {
		t.Errorf("runner 标识错: model=%q version=%q", r.ModelName, r.Version)
	}
	if r.FallbackName != "fb" || r.ReflectRounds != 2 || r.ReflectEvidence != "strict" || !r.CompletenessRetry {
		t.Errorf("配置透传错: %+v", r)
	}
	if len(r.SkillRefs) != 1 || r.SkillRefs[0] != "ocr-grading" {
		t.Errorf("compliance skillRefs 透传错: %v", r.SkillRefs)
	}
}

func TestBuildToolPoolEmptyDeclarations(t *testing.T) {
	pool, specs := buildToolPool(&config.Config{}, &plugin.PluginsFile{}, logx.Nop())
	if pool != nil || specs != nil {
		t.Fatalf("无 MCP 声明应返回 nil,nil: %v %v", pool, specs)
	}
}

func TestBuildSemSearchAndMemKernel(t *testing.T) {
	// 未开启语义检索/记忆内核：全 nil（运行期按未启用处理）。
	sys := &System{}
	if mgr := buildSemSearchAndMemKernel(&config.Config{}, logx.Nop(), sys); mgr != nil {
		t.Fatalf("未配置时应返回 nil manager, got %v", mgr)
	}
	if sys.MemKernel != nil {
		t.Fatal("未开启记忆内核时 MemKernel 应为 nil")
	}

	// 精确边开了但语义检索未就绪 → 仅告警，不装配。
	sys2 := &System{}
	if mgr := buildSemSearchAndMemKernel(&config.Config{GraphPrecise: true}, logx.Nop(), sys2); mgr != nil {
		t.Fatalf("GraphPrecise 单独开启不应装配 manager: %v", mgr)
	}

	// 记忆内核：临时目录内可建库并回填 sys.MemKernel。
	dir := t.TempDir()
	sys3 := &System{}
	buildSemSearchAndMemKernel(&config.Config{
		MemoryKernel: true, MemoryDir: dir, MemoryFTSTokenize: "trigram",
	}, logx.Nop(), sys3)
	if sys3.MemKernel == nil {
		t.Fatal("记忆内核应成功打开并回填")
	}
	if _, err := os.Stat(filepath.Join(dir, "kernel.db")); err != nil {
		t.Fatalf("kernel.db 未创建: %v", err)
	}
}

func TestSetupPIIPolicyGlobalSwitch(t *testing.T) {
	old := pii.Enabled()
	t.Cleanup(func() { pii.SetEnabled(old) })

	setupPIIPolicy(&config.Config{PIIMask: true}, logx.Nop())
	if !pii.Enabled() {
		t.Fatal("PIIMask=true 应启用脱敏策略")
	}
	setupPIIPolicy(&config.Config{PIIMask: false}, logx.Nop())
	if pii.Enabled() {
		t.Fatal("PIIMask=false 应关闭脱敏策略")
	}
}

func TestResolvePollTargets(t *testing.T) {
	if got := resolvePollTargets(&config.Config{}, logx.Nop()); got != nil {
		t.Fatalf("空 PollRepos 应返回 nil: %v", got)
	}
	got := resolvePollTargets(&config.Config{
		PollRepos: " Gitea:Owner/Repo , bad-item, github:o/r2 ",
	}, logx.Nop())
	if len(got) != 2 {
		t.Fatalf("合法目标数 = %d, want 2（bad-item 记录告警后丢弃）: %v", len(got), got)
	}
	if got[0].Platform != "gitea" || got[0].Owner != "Owner" || got[0].Repo != "Repo" {
		t.Errorf("目标解析错: %+v", got[0])
	}
	if got[1].Platform != "github" || got[1].Owner != "o" || got[1].Repo != "r2" {
		t.Errorf("目标解析错: %+v", got[1])
	}
}

func TestEnsureWebhooksAtStartupGates(t *testing.T) {
	// 行为断言：三个前置门都必须在抢租约之前返回。用真实 store 验证
	// 「门控生效」的可观测面——调用后同一 lease key 仍可被其他 holder 获取。
	st, err := storage.Open(filepath.Join(t.TempDir(), "bootstrap.db"))
	if err != nil {
		t.Fatalf("打开 store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	targets := []poller.Target{{Platform: "gitea", Owner: "o", Repo: "r"}}

	cases := []struct {
		name    string
		cfg     *config.Config
		targets []poller.Target
	}{
		{"feature_off", &config.Config{}, targets},
		{"public_url_missing", &config.Config{EnsureWebhook: true}, targets},
		{"no_targets", &config.Config{EnsureWebhook: true, PublicURL: "http://x"}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ensureWebhooksAtStartup(st, &System{}, c.cfg, logx.Nop(), c.targets)
			ok, err := st.TryAcquire(ctx, "webhook_ensure", "gate-test-holder", ensureLeaseTTL)
			if err != nil || !ok {
				t.Fatalf("门控应早于租约获取返回: ok=%v err=%v", ok, err)
			}
			if err := st.Release(ctx, "webhook_ensure", "gate-test-holder"); err != nil {
				t.Fatalf("释放租约: %v", err)
			}
		})
	}
}

func TestInitSDKAndWorkcopyCreatesIsolatedRoot(t *testing.T) {
	root := t.TempDir()
	sdk, wc, err := initSDKAndWorkcopy(&config.Config{WorkRoot: root}, logx.Nop(), "abc123")
	if err != nil {
		t.Fatalf("initSDKAndWorkcopy: %v", err)
	}
	if sdk == nil || wc == nil {
		t.Fatal("SDK/workcopy 必须装配")
	}
	if fi, err := os.Stat(filepath.Join(root, "instance-abc123")); err != nil || !fi.IsDir() {
		t.Fatalf("实例隔离工作目录未创建: %v", err)
	}
	if len(sdk.Ready()) != 0 {
		t.Fatalf("无平台配置时 Ready 应为空: %v", sdk.Ready())
	}
}
