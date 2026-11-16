package adapter_builtin

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"github.com/oagentkit/llm"
	"github.com/oagentkit/skillkit/skill"

	"github.com/yi-nology/argus/internal/dispatch"
	"github.com/yi-nology/argus/internal/domain"
	"github.com/yi-nology/argus/internal/plugin"
	"github.com/yi-nology/argus/internal/reporttmpl"
)

// fakeGen 最小 llm.Generator：GenerateJSON 把 canned JSON 解进 out（与
// FindingsWire 的 json 标签 findings/notes 对齐），可注入失败。
type fakeGen struct {
	jsonOut string
	err     error
	calls   int
	stage   string
}

func (f *fakeGen) Generate(context.Context, string, []*schema.Message) (*schema.Message, error) {
	return nil, nil
}

func (f *fakeGen) GenerateJSON(_ context.Context, stage string, _ []*schema.Message, out any) error {
	f.calls++
	f.stage = stage
	if f.err != nil {
		return f.err
	}
	return json.Unmarshal([]byte(f.jsonOut), out)
}

func (f *fakeGen) UsedTokens() int               { return 0 }
func (f *fakeGen) RawModel() model.BaseChatModel { return nil }

// NewReviewer 的 skillRefs 按 name@version 解析；无版本号时 Version 留空。
func TestNewReviewer_SkillRefParsing(t *testing.T) {
	sp := skill.NewFileProvider(t.TempDir())
	r, err := NewReviewer(plugin.AgentManifest{Name: "req-review"}, "correctness",
		&fakeGen{}, []string{"ocr-triage@v1.2", "plain-skill"}, sp)
	if err != nil {
		t.Fatalf("NewReviewer: %v", err)
	}
	if len(r.skillRefs) != 2 {
		t.Fatalf("skillRefs 数量 = %d, want 2", len(r.skillRefs))
	}
	if r.skillRefs[0].Name != "ocr-triage" || r.skillRefs[0].Version != "v1.2" {
		t.Fatalf("skillRefs[0] = %+v, want name=ocr-triage version=v1.2", r.skillRefs[0])
	}
	if r.skillRefs[1].Name != "plain-skill" || r.skillRefs[1].Version != "" {
		t.Fatalf("skillRefs[1] = %+v, want name=plain-skill version 空", r.skillRefs[1])
	}
}

// buildReviewSystem 单源拼装：strategy 段仅 ReAct 路径出现；skill 注入段带专用标题。
func TestBuildReviewSystem_Sections(t *testing.T) {
	r, err := NewReviewer(plugin.AgentManifest{Name: "req-review"}, "correctness",
		&fakeGen{}, nil, nil)
	if err != nil {
		t.Fatalf("NewReviewer: %v", err)
	}
	r.skillText = "SKILL-METHOD-TEXT"

	sys := r.buildReviewSystem(plugin.Task{}, "STRATEGY-TEXT")
	for _, want := range []string{"STRATEGY-TEXT", "工具使用策略", "SKILL-METHOD-TEXT", "审查方法论（skill 注入）"} {
		if !strings.Contains(sys, want) {
			t.Fatalf("buildReviewSystem(strategy) 缺 %q:\n%s", want, sys)
		}
	}

	bare := r.buildReviewSystem(plugin.Task{}, "")
	if strings.Contains(bare, "工具使用策略") {
		t.Fatalf("单轮路径不应出现工具使用策略段:\n%s", bare)
	}
	if !strings.Contains(bare, "SKILL-METHOD-TEXT") {
		t.Fatalf("skill 注入段两路径都应出现:\n%s", bare)
	}
}

// v6.10.0 报告契约模板：chain 纪律仅 analyst 模板追加；standard/minimal/缺键
// 与现状逐字节一致（既有行为零漂移由本 golden 钉住）。
func TestBuildReviewSystem_ReportTemplateChain(t *testing.T) {
	mk := func() *Reviewer {
		r, err := NewReviewer(plugin.AgentManifest{Name: "req-review"}, "correctness", &fakeGen{}, nil, nil)
		if err != nil {
			t.Fatalf("NewReviewer: %v", err)
		}
		return r
	}
	baseline := mk().buildReviewSystem(plugin.Task{}, "")
	if strings.Contains(baseline, "chain") {
		t.Fatalf("缺省模板不应含 chain 纪律:\n%s", baseline)
	}
	// standard/minimal/空键/非法键：全部与 baseline 逐字节一致。
	for _, key := range []string{"standard", "minimal", "", "bogus"} {
		task := plugin.Task{Artifacts: map[string]string{plugin.ArtifactReportTemplate: key}}
		if got := mk().buildReviewSystem(task, ""); got != baseline {
			t.Fatalf("模板键 %q 提示词与 baseline 漂移", key)
		}
	}
	// analyst：追加 chain 纪律。
	analyst := plugin.Task{Artifacts: map[string]string{plugin.ArtifactReportTemplate: "analyst"}}
	got := mk().buildReviewSystem(analyst, "")
	if !strings.HasSuffix(got, reporttmpl.ChainInstruction) {
		t.Fatalf("analyst 模板应追加 ChainInstruction:\n尾部=%q", got[len(got)-120:])
	}
	if !strings.Contains(got, "\"chain\"") {
		t.Fatalf("analyst chain 纪律缺字段锚点:\n%s", got)
	}
}

// v6.18.0 findings JSON 输出协议补 evidence 字段：Finding.Evidence 由模型在
// 协议里产出（可选），Finding 级交叉校验（VerifyFindingsEvidence）才有输入——
// 协议缺锚点 = schema 字段死代码。同时钉住「引用须在数据区变更文件上」的纪律句。
func TestBuildReviewSystem_EvidenceProtocol(t *testing.T) {
	r, err := NewReviewer(plugin.AgentManifest{Name: "req-review"}, "correctness", &fakeGen{}, nil, nil)
	if err != nil {
		t.Fatalf("NewReviewer: %v", err)
	}
	sys := r.buildReviewSystem(plugin.Task{}, "")
	if !strings.Contains(sys, `"evidence"`) {
		t.Fatalf("输出协议缺 evidence 字段锚点:\n%s", sys)
	}
	if !strings.Contains(sys, "四态核验") {
		t.Fatalf("缺 evidence 四态核验纪律句:\n%s", sys)
	}
	// ReAct 与单轮两路径同源（buildReviewSystem 单一出口），此处即双路径覆盖。
}

// invokeSingle 把 FindingsWire 解进结果并强制 By=manifest.Name；上游失败原样上抛。
func TestInvokeSingle_ByStampedAndErrorPropagation(t *testing.T) {
	g := &fakeGen{jsonOut: `{"findings":[{"file":"pkg/a.go","comment":"c1"}],"notes":"n1"}`}
	r, err := NewReviewer(plugin.AgentManifest{Name: "req-review"}, "security", g, nil, nil)
	if err != nil {
		t.Fatalf("NewReviewer: %v", err)
	}

	res, err := r.invokeSingle(context.Background(), plugin.Task{})
	if err != nil {
		t.Fatalf("invokeSingle: %v", err)
	}
	if g.calls != 1 || g.stage != "plugin:req-review" {
		t.Fatalf("调用记账 = calls:%d stage:%q, want 1 / plugin:req-review", g.calls, g.stage)
	}
	if len(res.Findings) != 1 || res.Findings[0].File != "pkg/a.go" {
		t.Fatalf("Findings = %+v, want 1 条 file=pkg/a.go", res.Findings)
	}
	if res.Findings[0].By != "req-review" {
		t.Fatalf("By = %q, want req-review（builtin 强制 manifest 名）", res.Findings[0].By)
	}
	if res.Notes != "n1" {
		t.Fatalf("Notes = %q, want n1", res.Notes)
	}

	// 负控：上游失败不得被吞成空结果。
	bad := &fakeGen{err: errors.New("upstream boom")}
	r2, _ := NewReviewer(plugin.AgentManifest{Name: "req-review"}, "security", bad, nil, nil)
	if res2, err2 := r2.invokeSingle(context.Background(), plugin.Task{}); err2 == nil {
		t.Fatalf("上游失败应返回错误，得到 %+v", res2)
	}
}

// buildReviewUser 数据区契约：路径纪律贴在 diff 围栏前（v3.51.0 回归锚），
// 摘要与编队黑板各自成围栏，空任务不注入任何段。
func TestBuildReviewUser_FencesAndDiscipline(t *testing.T) {
	r, err := NewReviewer(plugin.AgentManifest{Name: "req-review"}, "correctness",
		&fakeGen{}, nil, nil)
	if err != nil {
		t.Fatalf("NewReviewer: %v", err)
	}

	if got := r.buildReviewUser(plugin.Task{}); got != "" {
		t.Fatalf("空任务应得空串, got %q", got)
	}

	task := plugin.Task{
		Diff: &plugin.DiffContext{Text: "diff --git a/pkg/a.go b/pkg/a.go"},
		Artifacts: map[string]string{
			plugin.ArtifactChangeSummary: "SUM-TEXT",
			dispatch.ArtifactSquadBoard:  "BOARD-TEXT",
		},
	}
	user := r.buildReviewUser(task)
	iDisc := strings.Index(user, "路径纪律")
	iDiff := strings.Index(user, "diff --git a/pkg/a.go")
	if iDisc < 0 || iDiff < 0 || iDisc > iDiff {
		t.Fatalf("路径纪律必须出现在 diff 数据之前: disc=%d diff=%d\n%s", iDisc, iDiff, user)
	}
	if !strings.Contains(user, "SUM-TEXT") || !strings.Contains(user, "BOARD-TEXT") {
		t.Fatalf("缺变更摘要/编队黑板围栏:\n%s", user)
	}
	if !strings.Contains(user, "编队共识") {
		t.Fatalf("黑板段应带编队共识指令:\n%s", user)
	}
}

// ---------- CompliancePlugin 构造与 Invoke 映射 ----------

// fakeRunner 每次 Invoke 返回预设 WorkflowOutput（或错误）。
type fakeRunner struct {
	out *plugin.WorkflowOutput
	err error
}

func (f *fakeRunner) Run(context.Context, plugin.Task, *llm.Budget) (*plugin.WorkflowOutput, error) {
	return f.out, f.err
}

func TestNewCompliance_DefaultsAndNilFactory(t *testing.T) {
	if _, err := NewCompliance(plugin.AgentManifest{}, nil); err == nil {
		t.Fatal("nil factory 应报错")
	}
	p, err := NewCompliance(plugin.AgentManifest{}, func(*llm.Budget) plugin.WorkflowRunner {
		return &fakeRunner{}
	})
	if err != nil {
		t.Fatalf("NewCompliance: %v", err)
	}
	if p.Manifest().Timeout != 600*time.Second {
		t.Fatalf("缺省 Timeout = %v, want 600s", p.Manifest().Timeout)
	}
	if p.Manifest().Name != "requirement-compliance" || p.Manifest().Kind != plugin.KindBuiltin {
		t.Fatalf("缺省 Name/Kind = %q/%q", p.Manifest().Name, p.Manifest().Kind)
	}
}

func TestComplianceInvoke_MapsWorkflowOutput(t *testing.T) {
	factoryOut := &plugin.WorkflowOutput{
		Verdict: "comment",
		Rule:    "R5.3",
		Summary: "变更摘要文本",
		Report:  &plugin.ComplianceReport{Summary: "t"},
		Merged:  &plugin.MergedFindings{Findings: []domain.Finding{{File: "a.go", Comment: "c"}}},
	}
	p, err := NewCompliance(plugin.AgentManifest{Name: "req"}, func(budget *llm.Budget) plugin.WorkflowRunner {
		if budget == nil {
			t.Fatal("Task.Budget 应透传给 factory")
		}
		return &fakeRunner{out: factoryOut}
	})
	if err != nil {
		t.Fatal(err)
	}
	budget := llm.NewBudget(100)
	res, err := p.Invoke(context.Background(), plugin.Task{Budget: budget})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if res.Verdict != "comment" || len(res.Findings) != 1 || res.Findings[0].File != "a.go" {
		t.Fatalf("映射失真: %+v", res)
	}
	if res.Artifact[plugin.ArtifactRule] != "R5.3" || res.Artifact[plugin.ArtifactChangeSummary] != "变更摘要文本" {
		t.Fatalf("Artifact 映射失真: %+v", res.Artifact)
	}
	if !strings.Contains(res.Artifact[plugin.ArtifactComplianceReport], `"summary":"t"`) {
		t.Fatalf("报告 JSON 缺失: %q", res.Artifact[plugin.ArtifactComplianceReport])
	}

	// 上游失败原样上抛
	p2, _ := NewCompliance(plugin.AgentManifest{Name: "req"}, func(*llm.Budget) plugin.WorkflowRunner {
		return &fakeRunner{err: errors.New("workflow boom")}
	})
	if _, err := p2.Invoke(context.Background(), plugin.Task{}); err == nil {
		t.Fatal("workflow 失败应上抛")
	}
}
