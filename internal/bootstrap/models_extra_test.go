package bootstrap

// 模型装饰层补充：stageGenerator 单模型/降级链两形态的配置传递、
// wrapReactFailoverFn 的 nil 短路与切换观测回调。

import (
	"context"
	"testing"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"github.com/oagentkit/llm"
	"github.com/oekit/logx"

	"github.com/yi-nology/argus/internal/config"
	"github.com/yi-nology/argus/internal/obs"
)

// fakeProvider 只暴露 Provider 契约（Model 为 nil：构造装饰层不发网络/不调模型）。
type fakeProvider struct{ name string }

func (f *fakeProvider) Name() string                        { return f.name }
func (f *fakeProvider) Model() model.BaseChatModel          { return nil }
func (f *fakeProvider) ModelName() string                   { return f.name }
func (f *fakeProvider) ContextTokens() int                  { return 4096 }
func (f *fakeProvider) MaxOutputTokens() int                { return 512 }
func (f *fakeProvider) CostPer1KTokens() (float64, float64) { return 0, 0 }

func TestStageGeneratorNoFallbackUsesClientWithResilience(t *testing.T) {
	cfg := &config.Config{}
	cfg.LLM.ContextToken = 32000
	cfg.LLM.MaxTokens = 4096
	cfg.LLM.LLMResilienceRetries = 2
	cfg.LLM.LLMResilienceBaseDelay = 3 * time.Second

	g, err := stageGenerator(&fakeProvider{name: "primary"}, nil,
		&config.StageOverride{Model: "r1-model", ContextTokens: 8000}, cfg, nil)
	if err != nil {
		t.Fatalf("stageGenerator: %v", err)
	}
	c, ok := g.(*llm.Client)
	if !ok {
		t.Fatalf("无降级应返回 *llm.Client, got %T", g)
	}
	if c.ContextTokens != 8000 {
		t.Errorf("阶段窗口 = %d, want 8000", c.ContextTokens)
	}
	if c.MaxRetries != cfg.LLM.LLMResilienceRetries+1 {
		t.Errorf("MaxRetries = %d, want %d（env 语义不含首次）", c.MaxRetries, cfg.LLM.LLMResilienceRetries+1)
	}
	if c.BaseDelay != 3*time.Second {
		t.Errorf("BaseDelay = %v, want 3s", c.BaseDelay)
	}
	if c.ModelName != "r1-model" {
		t.Errorf("ModelName = %q, want r1-model", c.ModelName)
	}

	// 阶段未覆盖窗口 → 回落主配置窗口。
	g2, err := stageGenerator(&fakeProvider{name: "primary"}, nil,
		&config.StageOverride{Model: "r1-model"}, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if c2 := g2.(*llm.Client); c2.ContextTokens != 32000 {
		t.Errorf("未覆盖窗口应回落主配置 32000, got %d", c2.ContextTokens)
	}
}

func TestStageGeneratorWithFallbackBuildsResilient(t *testing.T) {
	cfg := &config.Config{}
	cfg.LLM.LLMResilienceRetries = 1
	g, err := stageGenerator(&fakeProvider{name: "primary"}, &fakeProvider{name: "fb"},
		&config.StageOverride{Model: "r2-model"}, cfg, nil)
	if err != nil {
		t.Fatalf("stageGenerator: %v", err)
	}
	if _, ok := g.(*llm.Resilient); !ok {
		t.Fatalf("有降级应返回 *llm.Resilient, got %T", g)
	}
}

func TestWrapReactFailoverFnNilPrimaryAndCallback(t *testing.T) {
	cfg := &config.Config{}
	cfg.LLM.Model = "main"
	cfg.LLM.FallbackModel = "fallback"
	wrap := wrapReactFailoverFn(cfg, &fakeChatModel{}, logx.Nop())

	if got := wrap(nil, "chat"); got != nil {
		t.Fatalf("nil 主模型应短路返回 nil, got %T", got)
	}

	fm, ok := wrap(&fakeChatModel{}, "R3").(*llm.FailoverModel)
	if !ok || fm == nil {
		t.Fatalf("非 nil 主模型应返回 *llm.FailoverModel, got %T", fm)
	}
	before := obs.LLMFailoverTotal()
	if fm.OnFailover == nil {
		t.Fatal("OnFailover 回调未接线（主备切换将无指标）")
	}
	fm.OnFailover("main", "fallback", "429")
	if got := obs.LLMFailoverTotal(); got != before+1 {
		t.Fatalf("切换后 LLMFailoverTotal = %d, want %d", got, before+1)
	}
}

// fakeChatModel 仅用于类型占位；测试不触发 Generate。
type fakeChatModel struct{}

func (f *fakeChatModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	return nil, nil
}

func (f *fakeChatModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return nil, nil
}
