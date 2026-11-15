package adapter_builtin

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"github.com/oagentkit/llm"
	"github.com/oagentkit/skillkit/skill"
	"github.com/oekit/logx"
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
