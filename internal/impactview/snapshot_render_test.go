// snapshot_render_test.go —— mermaid/列表/报告段/prompt 段渲染与确定性测试。

package impactview

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/oagentkit/textx/codeintel"
)

// TestRenderMermaid mermaid 形态：flowchart 头、changed 类、字符上限。
func TestRenderMermaid(t *testing.T) {
	dir := writeFixture(t)
	idx, _ := codeintel.NewIndex(dir, 2000)
	s := Build(context.Background(), Input{
		Dir: dir, ChangedFiles: []string{"pkga/a.go"},
		HunkRanges: changedOf("pkga/a.go"), Index: idx, MaxCommits: -1,
	})
	src, _ := RenderMermaidWithStats(s)
	if src == "" {
		t.Fatal("mermaid 为空")
	}
	if !strings.HasPrefix(src, "flowchart LR\n") {
		t.Fatalf("头不对: %s", src[:40])
	}
	if !strings.Contains(src, ":::changed") {
		t.Fatal("改动节点缺 changed 类")
	}
	if runeLen(src) > MaxMermaidRunes {
		t.Fatalf("超字符上限: %d", runeLen(src))
	}
	if strings.Count(src, "\n  n") > MaxMermaidNodes {
		t.Fatal("节点超上限")
	}
}

// TestRenderMermaidAggregation 巨量节点触发聚合仍不超限。
func TestRenderMermaidAggregation(t *testing.T) {
	// 构造 40 个受影响文件节点（远超 24 上限）
	s := &Snapshot{Version: SnapshotVersion, Changed: []string{"a.go"}}
	for i := range 40 {
		id := "file:pkg" + itoaN(i) + "/f.go"
		s.Nodes = append(s.Nodes, Node{ID: id, Kind: KindFile, Label: id})
		s.Edges = append(s.Edges, Edge{From: "file:a.go", To: id, Rel: RelCaller})
	}
	src, _ := RenderMermaidWithStats(s)
	if runeLen(src) > MaxMermaidRunes || strings.Count(src, "\n  n") > MaxMermaidNodes {
		t.Fatalf("聚合失败: len=%d", runeLen(src))
	}
}

// TestRenderList mermaid 降级列表形态。
func TestRenderList(t *testing.T) {
	dir := writeFixture(t)
	s := Build(context.Background(), Input{Dir: dir, ChangedFiles: []string{"pkga/a.go"}, MaxCommits: -1})
	l := RenderList(s)
	if !strings.Contains(l, "**改动**") || !strings.Contains(l, "pkga/a.go") {
		t.Fatalf("列表形态不对:\n%s", l)
	}
}

// TestRenderReportSection 报告段：details 折叠 + 风险徽标 + mermaid 或列表。
func TestRenderReportSection(t *testing.T) {
	dir := writeFixture(t)
	s := Build(context.Background(), Input{Dir: dir, ChangedFiles: []string{"pkga/a.go"}, MaxCommits: -1})
	sec := RenderReportSection(s)
	if !strings.Contains(sec, "<details>") || !strings.Contains(sec, "</details>") {
		t.Fatalf("缺 details 折叠:\n%s", sec)
	}
	if !strings.Contains(sec, "影响范围") {
		t.Fatal("缺标题")
	}
	if strings.Contains(sec, "```mermaid") == false && !strings.Contains(sec, "**改动**") {
		t.Fatal("mermaid 与列表应至少其一")
	}
}

// TestRenderPromptSection prompt 段：紧凑 + 纪律声明，单行可围栏。
func TestRenderPromptSection(t *testing.T) {
	dir := writeFixture(t)
	idx, _ := codeintel.NewIndex(dir, 2000)
	s := Build(context.Background(), Input{
		Dir: dir, ChangedFiles: []string{"pkga/a.go"},
		HunkRanges: changedOf("pkga/a.go"), Index: idx, MaxCommits: -1,
	})
	p := RenderPromptSection(s)
	if !strings.Contains(p, "影响面推导") || !strings.Contains(p, "使用纪律") {
		t.Fatalf("prompt 段缺要素:\n%s", p)
	}
	if len(p) > 2000 {
		t.Fatalf("prompt 段过长: %d", len(p))
	}
}

// TestDeterminism 同输入两次构建 → 快照 JSON 逐字节一致（GeneratedAt 除外）。
func TestDeterminism(t *testing.T) {
	dir := writeFixture(t)
	idx, _ := codeintel.NewIndex(dir, 2000)
	mk := func() *Snapshot {
		return Build(context.Background(), Input{
			Dir: dir, ChangedFiles: []string{"pkga/a.go"},
			HunkRanges: changedOf("pkga/a.go"), Index: idx, MaxCommits: -1,
		})
	}
	a, b := mk(), mk()
	a.GeneratedAt, b.GeneratedAt = time.Time{}, time.Time{}
	a.Stats.TookMS, b.Stats.TookMS = 0, 0 // 计时天然抖动，不入确定性口径
	ab, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	if string(ab) != string(bb) {
		t.Fatalf("快照不确定:\n%s\n%s", ab, bb)
	}
}

// TestMermaidEscape 标签转义：引号进 mermaid 源不破语法。
func TestMermaidEscape(t *testing.T) {
	got := mermaidEscape(`he said "hi" \ bye`)
	if strings.Count(got, `\"`) != 2 || strings.Count(got, `\\`) != 1 {
		t.Fatalf("escape = %s", got)
	}
}
