package requirement

import (
	"context"
	"testing"

	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/oagentkit/llm"
)

// ---------- R3.5 用量回灌任务预算（口径钉约） ----------

// usageEmitModel 按 eino-ext openai 的方式在 Generate 末尾发射 callbacks.OnEnd
// 用量（TokenUsage）——真实部署里模型组件必发；反思链的用量靠它进 runner.execContext
// 装配的 NewUsageHandler 回灌任务预算。
type usageEmitModel struct {
	*reflectFakeModel
	prompt, completion int
}

func (m *usageEmitModel) Generate(ctx context.Context, in []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	ctx = callbacks.EnsureRunInfo(ctx, "FakeChat", components.ComponentOfChatModel)
	out, err := m.reflectFakeModel.Generate(ctx, in, opts...)
	if err != nil {
		return nil, err
	}
	callbacks.OnEnd(ctx, &model.CallbackOutput{
		Message:    out,
		TokenUsage: &model.TokenUsage{PromptTokens: m.prompt, CompletionTokens: m.completion},
	})
	return out, nil
}

// 反思阶段 LLM 用量必须计入任务 token 预算（runner.execContext 的 UsageHandler
// 旁路回灌，与 ReAct/RawModel 同口径）。曾有「反思不计预算」的过期口径——本用例
// 钉约真实行为：初稿+批判 2 次调用全额入账。
func TestReflectFindings_UsageFeedsTaskBudget(t *testing.T) {
	m := &usageEmitModel{
		reflectFakeModel: &reflectFakeModel{
			draftReplies: []string{`[{"id":0,"file":"a.go","line":10,"severity":"high","comment":"空指针解引用"}]`},
			verdicts:     []string{`{"pass":true,"issues":[]}`},
		},
		prompt: 100, completion: 50,
	}
	budget := llm.NewBudget(1_000_000)
	ctx := callbacks.InitCallbacks(context.Background(), nil,
		llm.NewUsageHandler(func(rec llm.UsageRecord) {
			budget.Add(rec.PromptTokens + rec.CompletionTokens)
		}))
	w := &Workflow{ReflectRounds: 1, ReflectModel: m, Log: nopLogger{}}
	task, merged := reflectTaskFixture()

	if info := w.reflectFindings(ctx, task, "", merged); info == nil {
		t.Fatal("应执行反思")
	}
	// 初稿 + 批判共 2 次调用 × (100+50) token。
	if budget.Used() != 300 {
		t.Fatalf("反思用量应回灌任务预算: used=%d want=300", budget.Used())
	}
}
