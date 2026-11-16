package requirement

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudwego/eino/components/tool"
	"github.com/oekit/logx"

	"github.com/yi-nology/argus/internal/plugin"
	"github.com/yi-nology/argus/internal/semsearch"
)

// wcStubEmbedder 词袋向量化 stub（v6.1.0，仿 internal/semsearch stubEmbedder，
// requirement 包内本地定义——测试依赖不外溢）：词表三词正交三维、词内子串计数，
// 「纯 db 文本」与查询「db」相似度 1.0、「db+http 混合文本」约 0.7，排序可预期，
// 不碰网络。
type wcStubEmbedder struct{}

var wcStubVocab = []string{"db", "http", "parse"}

func (s *wcStubEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, 0, len(texts))
	for _, t := range texts {
		v := make([]float32, len(wcStubVocab))
		lower := strings.ToLower(t)
		for i, w := range wcStubVocab {
			v[i] = float32(strings.Count(lower, w))
		}
		out = append(out, v)
	}
	return out, nil
}

func (s *wcStubEmbedder) Dim() int { return len(wcStubVocab) }

// v6.1.0：Graph Searchable（embedder 非空）时 R3/R4 工具表挂 search_sem（审查
// 面，与 MCP 面同名不同执行面），纯图模式（NewIndex(nil)）不挂——get_trace/
// get_symbol_context 只看 Graph 非 nil，照常挂载（独立判空）。执行断言：命中
// 渲染（序号+path:line+symbol+score+600 rune 截断+数据区围栏）、符号命中附
// 调用邻接行（调用方/被调分组）、无命中提示文本非 error、topK 钳制（缺省 5/
// 上界 10/透传小值）。
func TestR3ToolsSearchSem(t *testing.T) {
	dir := t.TempDir()
	writeFile := func(rel, content string) {
		t.Helper()
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeFile("go.mod", "module wfsem\n\ngo 1.22\n")
	// QueryDB 纯 db 词袋（余弦 1.0 稳居首条），调用方 Handle / 被调 SaveDB
	// 齐备——首条命中即出「调用方/被调」双组邻接行。
	writeFile("db.go", `package app

// QueryDB 从 DB 读用户。
func QueryDB() {
	SaveDB()
}
`)
	writeFile("store.go", "package app\n\n// SaveDB 把用户写回 DB 存储（HTTP 层调用）。\nfunc SaveDB() {}\n")
	writeFile("api.go", "package app\n\n// Handle HTTP 入口，查 DB。\nfunc Handle() {\n\tQueryDB()\n}\n")
	// 凑 chunk 数（topK 上界钳制观察需要 >10 个含 db chunk）；文本混入 http 压低
	// 余弦，不与 QueryDB 争首条。
	for i := range 12 {
		writeFile(fmt.Sprintf("pad/p%02d.go", i),
			fmt.Sprintf("package pad\n\n// DBPad%02d 缓存 HTTP 会话。\nfunc DBPad%02d() {}\n", i, i))
	}
	stub := &wcStubEmbedder{}
	gx := semsearch.NewIndex(stub)
	if err := gx.Refresh(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	if !gx.Searchable() {
		t.Fatalf("NewIndex(stub)+Refresh 后应 Searchable=true")
	}

	names := func(w *Workflow) []string {
		tools, _, err := w.r3Tools(context.Background(), plugin.Task{}, nil)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, tl := range tools {
			info, err := tl.Info(context.Background())
			if err != nil {
				continue
			}
			out = append(out, info.Name)
		}
		return out
	}
	contains := func(list []string, want string) bool {
		for _, n := range list {
			if n == want {
				return true
			}
		}
		return false
	}
	w := &Workflow{
		Graph:            gx,
		Log:              logx.Nop(),
		GetFileMaxChars:  1000,
		GetIssueMaxChars: 1000,
		GetDiffMaxChars:  1000,
	}
	if !contains(names(w), "search_sem") {
		t.Fatalf("Searchable Graph 应挂 search_sem: %v", names(w))
	}
	tools, _, err := w.r3Tools(context.Background(), plugin.Task{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var sem tool.BaseTool
	for _, tl := range tools {
		if info, err := tl.Info(context.Background()); err == nil && info.Name == "search_sem" {
			sem = tl
			break
		}
	}
	run := func(argsJSON string) string {
		t.Helper()
		raw, err := sem.(tool.InvokableTool).InvokableRun(context.Background(), argsJSON)
		if err != nil {
			t.Fatalf("search_sem 查询类失败应回提示文本非 error: %v", err)
		}
		var out symOut
		if err := json.Unmarshal([]byte(raw), &out); err != nil {
			t.Fatalf("search_sem 输出非 JSON: %v\n%s", err, raw)
		}
		return out.Result
	}

	// 命中渲染：数据区围栏 + 首条 path:line+symbol+score + 邻接行（调用方/被调
	// 分组，根目录键无 dir 前缀）。QueryDB 纯词袋余弦 1.0 稳居 [1]。
	hit := run(`{"query":"db","topK":3}`)
	for _, want := range []string{
		"search_sem 检索结果（数据）", "命中 3 条", "[1] db.go:3 QueryDB score=1.000",
		"引用前须 get_file 核实", "邻接：调用方 Handle；被调 SaveDB",
	} {
		if !strings.Contains(hit, want) {
			t.Errorf("search_sem 命中渲染缺 %q:\n%s", want, hit)
		}
	}

	// topK 钳制：缺省 5（chunk 15 个 > 5，钳制生效才可能 5 条）、上界 10、
	// 小值透传。
	if got := run(`{"query":"db"}`); !strings.Contains(got, "命中 5 条") {
		t.Errorf("topK 缺省应钳 5:\n%s", got)
	}
	if got := run(`{"query":"db","topK":99}`); !strings.Contains(got, "命中 10 条") {
		t.Errorf("topK=99 应钳上界 10:\n%s", got)
	}
	if got := run(`{"query":"db","topK":2}`); !strings.Contains(got, "命中 2 条") {
		t.Errorf("topK=2 应透传:\n%s", got)
	}

	// 空索引（可检索但无内容）：无命中是正常查询结果——提示文本非 error。
	empty := semsearch.NewIndex(stub)
	if err := empty.Refresh(context.Background(), t.TempDir()); err != nil {
		t.Fatal(err)
	}
	emptyHit := run2(&Workflow{Graph: empty, Log: logx.Nop(),
		GetFileMaxChars: 1000, GetIssueMaxChars: 1000, GetDiffMaxChars: 1000}, `{"query":"db"}`)
	if !strings.Contains(emptyHit, "语义检索无命中") {
		t.Errorf("空索引应回无命中提示文本:\n%s", emptyHit)
	}

	// 纯图模式（NewIndex(nil)）：search_sem 不挂（挂了必然报「语义检索未启用」
	// 的死工具），get_trace/get_symbol_context 照挂（只看 Graph 非 nil）。
	pure := semsearch.NewIndex(nil)
	if err := pure.Refresh(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	if pure.Searchable() {
		t.Fatalf("NewIndex(nil) 应 Searchable=false")
	}
	pureNames := names(&Workflow{Graph: pure, Log: logx.Nop(),
		GetFileMaxChars: 1000, GetIssueMaxChars: 1000, GetDiffMaxChars: 1000})
	if contains(pureNames, "search_sem") {
		t.Errorf("纯图模式不应挂 search_sem: %v", pureNames)
	}
	if !contains(pureNames, "get_trace") || !contains(pureNames, "get_symbol_context") {
		t.Errorf("纯图模式应仍挂 get_trace/get_symbol_context: %v", pureNames)
	}
}

// run2 独立 Workflow 的 search_sem 执行（空索引降级断言用；与上方 run 分开是
// 因为工具闭包绑定各自 Workflow）。
func run2(w *Workflow, argsJSON string) string {
	tools, _, err := w.r3Tools(context.Background(), plugin.Task{}, nil)
	if err != nil {
		return "r3Tools 失败: " + err.Error()
	}
	for _, tl := range tools {
		if info, err := tl.Info(context.Background()); err == nil && info.Name == "search_sem" {
			raw, rerr := tl.(tool.InvokableTool).InvokableRun(context.Background(), argsJSON)
			if rerr != nil {
				return "InvokableRun 失败: " + rerr.Error()
			}
			var out symOut
			if jerr := json.Unmarshal([]byte(raw), &out); jerr != nil {
				return "输出非 JSON: " + jerr.Error()
			}
			return out.Result
		}
	}
	return "search_sem 未挂载"
}
