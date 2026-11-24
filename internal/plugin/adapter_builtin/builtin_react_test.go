package adapter_builtin

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"github.com/oagentkit/llm"
	"github.com/oagentkit/skillkit/skill"
	"github.com/oekit/logx"
	"github.com/yi-nology/argus/internal/agentstream"
	"github.com/yi-nology/argus/internal/plugin"
)

// ---------- Reviewer 选项与 ReAct 模型解析 ----------

// fakeInvokableTool 最小 eino InvokableTool（ReAct 工具表非空用）。
type fakeInvokableTool struct{ name string }

func (f *fakeInvokableTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: f.name}, nil
}
func (f *fakeInvokableTool) InvokableRun(context.Context, string, ...tool.Option) (string, error) {
	return `{}`, nil
}

type errModel struct{}

func (errModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	return nil, errors.New("model down")
}
func (errModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return nil, errors.New("model down")
}

// streamChatModel 可控流式/非流式模型：记录 agentrun 实际走哪条路径。
type streamChatModel struct {
	mu            sync.Mutex
	chunks        []*schema.Message
	generateCalls int
	streamCalls   int
}

func (m *streamChatModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	m.mu.Lock()
	m.generateCalls++
	m.mu.Unlock()
	return schema.ConcatMessages(m.chunks)
}

func (m *streamChatModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	m.mu.Lock()
	m.streamCalls++
	m.mu.Unlock()
	return schema.StreamReaderFromArray(m.chunks), nil
}

func (m *streamChatModel) counts() (generate, stream int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.generateCalls, m.streamCalls
}

// DisableStreaming 的选择由 Task.DeltaSink 驱动：nil = Generate（旧整段），
// 非 nil = agentrun 原生流（Stream）并把权威快照交给 DeltaSink。
func TestInvokeReAct_StreamingDrivenByDeltaSink(t *testing.T) {
	const findingsJSON = `{"findings":[{"file":"a.go","comment":"c"}],"notes":""}`
	hook := func(context.Context, plugin.Task, *llm.Budget) ([]tool.BaseTool, string, error) {
		return []tool.BaseTool{&fakeInvokableTool{name: "noop"}}, "策略", nil
	}
	newReviewer := func(m model.BaseChatModel) *Reviewer {
		r, err := NewReviewer(plugin.AgentManifest{Name: "req"}, "sec", &fakeGen{}, nil, nil,
			WithToolsHook(hook), WithReactModel(m), WithLogger(logx.Nop()))
		if err != nil {
			t.Fatal(err)
		}
		return r
	}

	t.Run("DeltaSink nil 走整段", func(t *testing.T) {
		m := &streamChatModel{chunks: []*schema.Message{
			{Role: schema.Assistant, Content: findingsJSON},
		}}
		res, err := newReviewer(m).Invoke(context.Background(), plugin.Task{})
		if err != nil {
			t.Fatalf("Invoke: %v", err)
		}
		if len(res.Findings) != 1 {
			t.Fatalf("findings 解析失真: %+v", res)
		}
		if generate, stream := m.counts(); stream != 0 || generate == 0 {
			t.Fatalf("DeltaSink nil 应只走 Generate: generate=%d stream=%d", generate, stream)
		}
	})

	t.Run("DeltaSink 非 nil 走流式并收敛", func(t *testing.T) {
		m := &streamChatModel{chunks: []*schema.Message{
			{Role: schema.Assistant, ReasoningContent: "分析中", Content: findingsJSON},
		}}
		var deltas []plugin.AgentDelta
		res, err := newReviewer(m).Invoke(context.Background(), plugin.Task{
			DeltaSink: func(d plugin.AgentDelta) { deltas = append(deltas, d) },
		})
		if err != nil {
			t.Fatalf("Invoke: %v", err)
		}
		if len(res.Findings) != 1 {
			t.Fatalf("findings 解析失真: %+v", res)
		}
		if generate, stream := m.counts(); stream == 0 || generate != 0 {
			t.Fatalf("DeltaSink 非 nil 应走 Stream: generate=%d stream=%d", generate, stream)
		}
		var sawReasoning, sawText bool
		for _, d := range deltas {
			if !d.Final {
				continue
			}
			switch d.Phase {
			case "reasoning":
				sawReasoning = true
			case "text":
				sawText = true
				if d.Text != findingsJSON {
					t.Fatalf("text 权威快照不符: %q", d.Text)
				}
			}
		}
		if !sawReasoning || !sawText {
			t.Fatalf("应收到 reasoning/text 权威快照: %+v", deltas)
		}
	})
}

func TestReviewerOptionsAndReactModelResolution(t *testing.T) {
	r, err := NewReviewer(plugin.AgentManifest{Name: "req"}, "sec", &fakeGen{}, nil, nil,
		WithLogger(logx.Nop()),
		WithReactMaxIterations(3),
		WithReactModel(errModel{}),
		WithToolsHook(func(context.Context, plugin.Task, *llm.Budget) ([]tool.BaseTool, string, error) {
			return nil, "", nil
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	if r.reactMaxIterations != 3 || r.log == nil || r.toolsHook == nil {
		t.Fatalf("选项未生效: %+v", r)
	}
	if _, ok := r.reviewerReactModel().(errModel); !ok {
		t.Fatalf("WithReactModel 应优先于 RawModel: %T", r.reviewerReactModel())
	}
	// 未注入 reactModel 时回落 llm.RawModel()（fakeGen 返回 nil）
	r2, _ := NewReviewer(plugin.AgentManifest{Name: "req"}, "sec", &fakeGen{}, nil, nil)
	if r2.reviewerReactModel() != nil {
		t.Fatalf("无覆盖且 RawModel nil 应为 nil: %T", r2.reviewerReactModel())
	}
}

// ReAct 三路失败都必须降级单轮并在 Notes 留降级标记——审查不因工具链故障而失败。
func TestInvoke_ReActFailuresDegradeToSingle(t *testing.T) {
	cases := []struct {
		name string
		hook plugin.ToolsHook
	}{
		{"工具表构造失败", func(context.Context, plugin.Task, *llm.Budget) ([]tool.BaseTool, string, error) {
			return nil, "", errors.New("hook boom")
		}},
		{"工具表为空", func(context.Context, plugin.Task, *llm.Budget) ([]tool.BaseTool, string, error) {
			return nil, "", nil
		}},
		{"ReAct 运行失败", func(context.Context, plugin.Task, *llm.Budget) ([]tool.BaseTool, string, error) {
			return []tool.BaseTool{&fakeInvokableTool{name: "noop"}}, "策略", nil
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := &fakeGen{jsonOut: `{"findings":[{"file":"a.go","comment":"c"}],"notes":""}`}
			r, err := NewReviewer(plugin.AgentManifest{Name: "req"}, "sec", g, nil, nil,
				WithToolsHook(tc.hook), WithReactModel(errModel{}),
				WithLogger(logx.Nop()))
			if err != nil {
				t.Fatal(err)
			}
			res, err := r.Invoke(context.Background(), plugin.Task{})
			if err != nil {
				t.Fatalf("降级后单轮仍失败: %v", err)
			}
			if len(res.Findings) != 1 || res.Findings[0].By != "req" {
				t.Fatalf("单轮降级结果失真: %+v", res)
			}
			if !strings.Contains(res.Notes, plugin.DegradationMarker) {
				t.Fatalf("Notes 应带降级标记: %q", res.Notes)
			}
		})
	}
}

// ---------- ensureSkillText 惰性解析与缓存 ----------

func TestEnsureSkillText_ResolvesAndCaches(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "ocr.md"), []byte("# OCR 方法论\n核对证据链\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sp := skill.NewFileProvider(root)
	r, err := NewReviewer(plugin.AgentManifest{Name: "req"}, "sec", &fakeGen{}, []string{"ocr"}, sp)
	if err != nil {
		t.Fatal(err)
	}
	r.ensureSkillText(context.Background())
	got := r.skillPromptText()
	if !strings.Contains(got, "核对证据链") || !strings.Contains(got, "checksum=") {
		t.Fatalf("skill 文本应带内容与 checksum 头: %q", got)
	}
	// 10 分钟缓存期内不再解析（改写文件不生效）
	if err := os.WriteFile(filepath.Join(root, "ocr.md"), []byte("# 新版\nNEW-CONTENT\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r.ensureSkillText(context.Background())
	if strings.Contains(r.skillPromptText(), "NEW-CONTENT") {
		t.Fatal("缓存期内不应重新解析")
	}

	// 无 provider/refs 时为 no-op
	bare, _ := NewReviewer(plugin.AgentManifest{Name: "req"}, "sec", &fakeGen{}, []string{"x"}, nil)
	bare.ensureSkillText(context.Background())
	if bare.skillPromptText() != "" {
		t.Fatalf("无 provider 应保持空: %q", bare.skillPromptText())
	}
}

// ---------- agentloop 迁移的守卫分支（覆盖门：空答/回喂重试/解析/转换） ----------

// infoOnlyTool 只有元数据无可执行体（触发 ToPipelineTools 装配错误）。
type infoOnlyTool struct{}

func (infoOnlyTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: "meta-only", Desc: "d"}, nil
}

// TestInvokeReAct_EmptyFinalRetryThenSingle 空终答触发回喂重试（两次皆空）
// → 空回答错误 → Invoke 降级单轮（降级标记在案）。
func TestInvokeReAct_EmptyFinalRetryThenSingle(t *testing.T) {
	m := &streamChatModel{chunks: []*schema.Message{{Role: schema.Assistant, Content: ""}}}
	g := &fakeGen{jsonOut: `{"findings":[{"file":"a.go","comment":"c"}],"notes":""}`}
	r, err := NewReviewer(plugin.AgentManifest{Name: "req"}, "sec", g, nil, nil,
		WithToolsHook(func(context.Context, plugin.Task, *llm.Budget) ([]tool.BaseTool, string, error) {
			return []tool.BaseTool{&fakeInvokableTool{name: "noop"}}, "策略", nil
		}),
		WithReactModel(m), WithLogger(logx.Nop()))
	if err != nil {
		t.Fatal(err)
	}
	res, err := r.Invoke(context.Background(), plugin.Task{})
	if err != nil {
		t.Fatalf("应降级单轮而非失败: %v", err)
	}
	if len(res.Findings) != 1 || !strings.Contains(res.Notes, plugin.DegradationMarker) {
		t.Fatalf("空答应降级单轮: %+v", res)
	}
}

// TestInvokeReAct_FailureResetsStream DeltaSink 非 nil 且双跑皆败：回喂
// 重试边界发 reset（消费方清空累积），随后降级单轮。
func TestInvokeReAct_FailureResetsStream(t *testing.T) {
	g := &fakeGen{jsonOut: `{"findings":[{"file":"a.go","comment":"c"}],"notes":""}`}
	r, err := NewReviewer(plugin.AgentManifest{Name: "req"}, "sec", g, nil, nil,
		WithToolsHook(func(context.Context, plugin.Task, *llm.Budget) ([]tool.BaseTool, string, error) {
			return []tool.BaseTool{&fakeInvokableTool{name: "noop"}}, "策略", nil
		}),
		WithReactModel(errModel{}), WithLogger(logx.Nop()))
	if err != nil {
		t.Fatal(err)
	}
	var deltas []plugin.AgentDelta
	res, err := r.Invoke(context.Background(), plugin.Task{
		DeltaSink: func(d plugin.AgentDelta) { deltas = append(deltas, d) },
	})
	if err != nil {
		t.Fatalf("应降级单轮而非失败: %v", err)
	}
	if !strings.Contains(res.Notes, plugin.DegradationMarker) {
		t.Fatalf("应带降级标记: %+v", res)
	}
	var sawReset bool
	for _, d := range deltas {
		if d.Phase == agentstream.PhaseReset {
			sawReset = true
		}
	}
	if !sawReset {
		t.Fatalf("回喂重试应发 reset: %+v", deltas)
	}
}

// TestInvokeReAct_ParseFailDegradesToSingle 终答非空但非合法 JSON → 解析
// 错误 → 降级单轮（解析失败与运行失败同走降级面）。
func TestInvokeReAct_ParseFailDegradesToSingle(t *testing.T) {
	m := &streamChatModel{chunks: []*schema.Message{{Role: schema.Assistant, Content: "not-json"}}}
	g := &fakeGen{jsonOut: `{"findings":[{"file":"a.go","comment":"c"}],"notes":""}`}
	r, err := NewReviewer(plugin.AgentManifest{Name: "req"}, "sec", g, nil, nil,
		WithToolsHook(func(context.Context, plugin.Task, *llm.Budget) ([]tool.BaseTool, string, error) {
			return []tool.BaseTool{&fakeInvokableTool{name: "noop"}}, "策略", nil
		}),
		WithReactModel(m), WithLogger(logx.Nop()))
	if err != nil {
		t.Fatal(err)
	}
	res, err := r.Invoke(context.Background(), plugin.Task{})
	if err != nil {
		t.Fatalf("应降级单轮而非失败: %v", err)
	}
	if len(res.Findings) != 1 || !strings.Contains(res.Notes, plugin.DegradationMarker) {
		t.Fatalf("解析失败应降级单轮: %+v", res)
	}
}

// TestInvokeReAct_RunAttemptFailuresDegrade attempt 内的装配失败面：
// ToPipelineTools 非 Invokable / attempt 内 hook 瞬断 / 工具重名（pipeline
// 装配错）——都必须回喂重试后降级，不外抛。
func TestInvokeReAct_RunAttemptFailuresDegrade(t *testing.T) {
	cases := []struct {
		name string
		hook plugin.ToolsHook
	}{
		{"工具转换失败", func(context.Context, plugin.Task, *llm.Budget) ([]tool.BaseTool, string, error) {
			return []tool.BaseTool{infoOnlyTool{}}, "策略", nil
		}},
		{"attempt内hook瞬断", func(context.Context, plugin.Task, *llm.Budget) ([]tool.BaseTool, string, error) {
			return []tool.BaseTool{&fakeInvokableTool{name: "noop"}}, "策略", nil
		}},
		{"工具重名", func(context.Context, plugin.Task, *llm.Budget) ([]tool.BaseTool, string, error) {
			return []tool.BaseTool{&fakeInvokableTool{name: "dup"}, &fakeInvokableTool{name: "dup"}}, "策略", nil
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := &fakeGen{jsonOut: `{"findings":[{"file":"a.go","comment":"c"}],"notes":""}`}
			hook := tc.hook
			if tc.name == "attempt内hook瞬断" {
				// 预检（第 1 次）过、attempt 内（第 2 次起）断——覆盖 run 内
				// toolsHook 错误分支。
				calls := 0
				inner := hook
				hook = func(ctx context.Context, task plugin.Task, b *llm.Budget) ([]tool.BaseTool, string, error) {
					calls++
					if calls >= 2 {
						return nil, "", errors.New("hook transient")
					}
					return inner(ctx, task, b)
				}
			}
			r, err := NewReviewer(plugin.AgentManifest{Name: "req"}, "sec", g, nil, nil,
				WithToolsHook(hook), WithReactModel(errModel{}), WithLogger(logx.Nop()))
			if err != nil {
				t.Fatal(err)
			}
			res, err := r.Invoke(context.Background(), plugin.Task{})
			if err != nil {
				t.Fatalf("应降级单轮而非失败: %v", err)
			}
			if len(res.Findings) != 1 || !strings.Contains(res.Notes, plugin.DegradationMarker) {
				t.Fatalf("装配失败应降级单轮: %+v", res)
			}
		})
	}
}
