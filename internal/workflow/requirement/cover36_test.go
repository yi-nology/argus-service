// cover36_test.go —— 工作流纯面与可独立装配面补测（#36 kernel 60/70）：
// diff 路径集、任务类型技能词表、R1 来源锚渲染、stash 超限两级化（含 spill
// 存档取回指引）、反思模型解析链、R3 解析对齐闭包、外部工具注解包装、
// asInvokable 断言、审查工具表导出。
package requirement

import (
	"context"
	"strings"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"github.com/oekit/logx"

	"github.com/yi-nology/argus/internal/domain"
	"github.com/yi-nology/argus/internal/plugin"
)

// stubModel 最小模型桩（RawModel 解析链用）。
type coverStubModel struct{}

func (coverStubModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	return nil, context.Canceled
}
func (coverStubModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return nil, context.Canceled
}

// coverBaseTool 只实现 BaseTool（Info），不可调用——asInvokable 应 nil。
type coverBaseTool struct{}

func (coverBaseTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: "base"}, nil
}

// coverInvokableTool BaseTool + InvokableRun——asInvokable 应命中。
type coverInvokableTool struct{ coverBaseTool }

func (coverInvokableTool) InvokableRun(context.Context, string, ...tool.Option) (string, error) {
	return "", nil
}

func TestDiffPathsAndSkillsForKind(t *testing.T) {
	if diffPaths(nil) != nil {
		t.Fatal("nil diff 应 nil")
	}
	dc := &plugin.DiffContext{Files: []plugin.DiffFile{{Path: "a.go"}, {Path: "b.md"}}}
	if got := diffPaths(dc); len(got) != 2 || got[0] != "a.go" {
		t.Fatalf("diffPaths 失真: %v", got)
	}
	if got := skillsForKind(domain.TaskKindVisualCheck); len(got) != 1 || got[0] != plugin.SkillVisualCheck {
		t.Fatalf("visual_check 技能词表失真: %v", got)
	}
	if got := skillsForKind(domain.TaskKindCodeReview); len(got) != 1 || got[0] != plugin.SkillCodeReview {
		t.Fatalf("缺省技能词表失真: %v", got)
	}
}

func TestRefs2Source(t *testing.T) {
	if refs2source(nil) != "" {
		t.Fatal("空 refs 应空")
	}
	if got := refs2source([]string{"7", "9"}); got != "issue #7,issue #9" {
		t.Fatalf("refs2source 失真: %q", got)
	}
}

func TestStashOverflowAt(t *testing.T) {
	w := &Workflow{Log: logx.Nop()}
	// 未超限直通。
	if got := w.stashOverflowAt("short", 100); got != "short" {
		t.Fatalf("未超限应原样: %q", got)
	}
	// max<=0 直通。
	if got := w.stashOverflowAt("x", 0); got != "x" {
		t.Fatal("max<=0 应直通")
	}
	// 超限但无 SpillDir → mkdir 失败降级纯截断。
	big := strings.Repeat("x", 300)
	got := w.stashOverflowAt(big, 100)
	if !strings.Contains(got, "已丢弃") {
		t.Fatalf("无存档应如实告知丢弃: %q", got)
	}
	// 超限 + SpillDir 就绪 → get_detail 取回指引。
	w.SpillDir = t.TempDir()
	got2 := w.stashOverflowAt(big, 100)
	if !strings.Contains(got2, "get_detail") || !strings.Contains(got2, "ref=") {
		t.Fatalf("存档应给取回指引: %q", got2)
	}
}

func TestReflectModelChain(t *testing.T) {
	// 反思专用 → R3 → LLM RawModel → nil 四级解析。
	w1 := &Workflow{ReflectModel: coverStubModel{}}
	if w1.reflectModel() == nil {
		t.Fatal("专用反思模型应优先")
	}
	w2 := &Workflow{R3ChatModel: coverStubModel{}}
	if w2.reflectModel() == nil {
		t.Fatal("R3 模型次优")
	}
	w3 := &Workflow{LLM: nil}
	if w3.reflectModel() != nil {
		t.Fatal("全空应 nil")
	}
}

func TestAsInvokable(t *testing.T) {
	if asInvokable(coverBaseTool{}) != nil {
		t.Fatal("纯 BaseTool 应 nil（外部限制包装跳过）")
	}
	if asInvokable(coverInvokableTool{}) == nil {
		t.Fatal("InvokableTool 应命中断言")
	}
}

// TestR3ParseAligned 解析对齐闭包：合法报告解析 + 编造条目对齐剔除 + 坏 JSON
// 拒绝；证据路径核对集以变更文件为参照系。
func TestR3ParseAligned(t *testing.T) {
	w := &Workflow{Log: logx.Nop()}
	task := plugin.Task{Diff: &plugin.DiffContext{Files: []plugin.DiffFile{{Path: "a.go"}}}}
	spec := &plugin.RequirementSpec{ACs: []plugin.AcceptanceCriterion{{ID: "AC1", Text: "支持重试"}}}
	parse := w.r3ParseAligned(spec, task)

	// 合法报告（证据落变更文件内）。
	good := `{"checks":[{"id":"AC1","status":"implemented","evidence":"a.go:1 retry"}]}`
	rep, _, ok := parse(good)
	if !ok || len(rep.Checks) != 1 {
		t.Fatalf("合法报告应解析: %+v ok=%v", rep, ok)
	}
	// 编造条目（spec 外 AC）被双向对齐剔除。
	invented := `{"checks":[{"id":"ACX","status":"implemented","evidence":"a.go:1"},{"id":"AC1","status":"missing"}]}`
	rep2, _, ok2 := parse(invented)
	if !ok2 || len(rep2.Checks) != 1 {
		t.Fatalf("编造条目应剔除: %+v ok=%v", rep2, ok2)
	}
	// 坏 JSON 拒绝。
	if _, _, ok3 := parse("not json"); ok3 {
		t.Fatal("坏 JSON 应拒绝")
	}
}

// TestWorkflowLoadRepoConfigInjected 注入的 RepoConfig 优先（不再打平台）。
func TestWorkflowLoadRepoConfigInjected(t *testing.T) {
	w := &Workflow{RepoConfig: &plugin.RepoConfig{Loaded: true}, Log: logx.Nop()}
	rc := w.loadRepoConfig(context.Background(), plugin.Task{})
	if rc == nil || !rc.Loaded {
		t.Fatal("注入配置应直通")
	}
}

// TestWorkflowSkillTextInjection 技能文本经 skillText 注入（Runner 侧闭包单测
// 的 workflow 侧配对：字段透传）。
func TestWorkflowSkillTextInjection(t *testing.T) {
	w := &Workflow{
		LLM:             nil,
		SkillText:       func(context.Context) string { return "--- skill:m (checksum=ab) ---\n正文" },
		Log:             logx.Nop(),
		ReflectRounds:   2,
		ReflectEvidence: "soft",
	}
	if got := w.SkillText(context.Background()); !strings.Contains(got, "checksum=ab") {
		t.Fatalf("技能文本闭包失真: %q", got)
	}
	if w.ReflectRounds != 2 || w.ReflectEvidence != "soft" {
		t.Fatal("反思配置透传失真")
	}
	if skillsForKind("") == nil {
		t.Fatal("未知 kind 落缺省技能")
	}
}

// TestWorkflowDefaults 外部调用上限缺省（0 = r3AddExternalTools 兜底 5）。
func TestWorkflowDefaults(t *testing.T) {
	w := &Workflow{Log: logx.Nop()}
	if w.ExternalToolCallLimit != 0 {
		t.Fatal("缺省外部调用上限 0")
	}
}
