// qa_cover36_test.go —— #36 kernel 60/70 低覆盖面补测（行为钉住，不凑数）：
//   - diffFileTools 两个工具执行体（围栏/行号/错误回注/截断）
//   - similarIssueTool 执行体（映射/nil 跳过/错误回注/平台无 SearchManager 收窄）
//   - workcopyDir 就绪与降级（file:// 本地 git 夹具，零网络）
//   - SearchCode / SearchSem / Trace / SymContext 启用路径（确定性 bag embedder
//     + 纯图 Manager，不触网）
//   - ptcAsk 全分支（合成/执行/修复回喂/预算截停/作答失败回退）
//   - persistTurn 乐观锁冲突重放（正常重放 / 重读坏 JSON 降级）
package qa

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudwego/eino/components/tool"

	"github.com/oagentkit/tools/workcopy"
	"github.com/oekit/logx"
	"github.com/yi-nology/go-git-platform/provider"
	"github.com/yi-nology/goagent-kernel/ptc"

	"github.com/yi-nology/argus/internal/semsearch"
	"github.com/yi-nology/argus/internal/storage"
)

// ---------- file:// 本地 git 夹具（照搬 workcopy 上游测试形态，零网络） ----------

func qaGitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=t@t",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// qaFixtureRepo 本地 git fixture：main 提交 files 内文件 + refs/pull/7/head
// 供 workcopy fetch（qa workcopyDir 固定 number=7）。
func qaFixtureRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	qaGitRun(t, dir, "init", "-b", "main", dir)
	for name, body := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	qaGitRun(t, dir, "add", ".")
	if len(files) == 0 {
		qaGitRun(t, dir, "commit", "--allow-empty", "-m", "init")
	} else {
		qaGitRun(t, dir, "commit", "-m", "init")
	}
	qaGitRun(t, dir, "update-ref", "refs/pull/7/head", "main")
	return dir
}

// qaPool file:// 凭证的零网络工作副本池。
func qaPool(t *testing.T, fixture string) *workcopy.Pool {
	t.Helper()
	p := workcopy.NewPool(t.TempDir(), logx.Nop())
	p.CredentialOf = func(string) (string, string, bool) { return "file://" + fixture, "x", true }
	return p
}

// ---------- diffFileTools 执行体 ----------

// get_diff：RawDiff 经 DataFence 包裹；GetCRDiff 为 nil 时安全产出空数据区。
func TestDiffFileTools_GetDiff(t *testing.T) {
	e, _, fp := newTestEngine(t, &fakeModel{})
	ctx := context.Background()
	getDiff, _, err := e.diffFileTools(ctx, "gitea", "acme", "app", "7")
	if err != nil {
		t.Fatal(err)
	}
	res, err := getDiff.(tool.InvokableTool).InvokableRun(ctx, "{}")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res, "+func login() {}") || !strings.Contains(res, "get_diff（数据）") {
		t.Fatalf("get_diff 应含 RawDiff 与围栏:\n%s", res)
	}
	// diff 为 nil（平台异常兜底形态）：空数据区安全产出，不 panic 不报错
	//（DataFence 对空文本不包围栏，出口恒为合法 {"diff":""}）。
	fp.diff = nil
	res2, err := getDiff.(tool.InvokableTool).InvokableRun(ctx, "{}")
	if err != nil || !strings.Contains(res2, `"diff":""`) {
		t.Fatalf("nil diff 应安全: err=%v res=%q", err, res2)
	}
}

// get_file：行号前缀先于截断；文件缺失走错误回注（不中断 ReAct）；超长截断。
func TestDiffFileTools_GetFile(t *testing.T) {
	e, _, fp := newTestEngine(t, &fakeModel{})
	ctx := context.Background()
	_, getFile, err := e.diffFileTools(ctx, "gitea", "acme", "app", "7")
	if err != nil {
		t.Fatal(err)
	}
	inv := getFile.(tool.InvokableTool)

	res, err := inv.InvokableRun(ctx, `{"path":"main.go"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res, "1|package main") || !strings.Contains(res, "get_file 文件内容") {
		t.Fatalf("get_file 应带行号与围栏:\n%s", res)
	}

	// 缺失文件：错误回注（提醒信封）而非 error——信封标记 + 平台错误文案都在。
	resMiss, err := inv.InvokableRun(ctx, `{"path":"nope.go"}`)
	if err != nil || !strings.Contains(resMiss, "tool-error") || !strings.Contains(resMiss, "file not found") {
		t.Fatalf("缺失文件应错误回注: err=%v res=%.160s", err, resMiss)
	}

	// 超长（>20000 rune）截断：行号已加，尾部带截断标记。
	fp.files["big.go"] = strings.Repeat("长", 25_000)
	resBig, err := inv.InvokableRun(ctx, `{"path":"big.go"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resBig, "…（截断）") {
		t.Fatal("超长文件应截断")
	}
	if strings.Count(resBig, "长") > 20_100 {
		t.Fatalf("截断后不应保留全文: %d", strings.Count(resBig, "长"))
	}
}

// ---------- similarIssueTool 执行体 ----------

// smPlatform 在 fakePlatform 上补 SearchManager 能力（type assertion 命中路）。
type smPlatform struct {
	*fakePlatform
	res []*provider.SearchIssueResult
	err error
}

func (f *smPlatform) SearchIssues(_ context.Context, opts provider.SearchIssuesOptions) ([]*provider.SearchIssueResult, *int, error) {
	if f.err != nil {
		return nil, nil, f.err
	}
	if opts.Repo != "acme/app" {
		return nil, nil, fmt.Errorf("repo 参数失真: %q", opts.Repo)
	}
	n := len(f.res)
	return f.res, &n, nil
}

func (f *smPlatform) SearchRepos(context.Context, provider.SearchReposOptions) ([]*provider.SearchRepoResult, *int, error) {
	return nil, nil, nil
}

func (f *smPlatform) SearchUsers(context.Context, provider.SearchUsersOptions) ([]*provider.SearchUserResult, *int, error) {
	return nil, nil, nil
}

func TestSimilarIssueTool(t *testing.T) {
	e, _, _ := newTestEngine(t, &fakeModel{})
	// 平台无 SearchManager：工具面自然收窄（nil, nil）。
	if si, err := e.similarIssueTool("gitea", "acme", "app"); err != nil || si != nil {
		t.Fatalf("无 SearchManager 应 (nil, nil): %v %v", si, err)
	}

	sp := &smPlatform{fakePlatform: newFakePlatform(), res: []*provider.SearchIssueResult{
		{Number: "12", Title: "登录 500", State: provider.IssueStateOpen, WebURL: "http://x/12", Labels: []string{"bug"}},
		nil, // 平台脏数据：跳过不炸
	}}
	e.SDK.RegisterProvider("gitea", sp)
	si, err := e.similarIssueTool("gitea", "acme", "app")
	if err != nil || si == nil {
		t.Fatalf("有 SearchManager 应返回工具: %v %v", si, err)
	}
	out, err := si.(tool.InvokableTool).InvokableRun(context.Background(), `{"keywords":"登录 500"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"number":"12"`) || !strings.Contains(out, `"title":"登录 500"`) ||
		!strings.Contains(out, `"labels":["bug"]`) {
		t.Fatalf("结果映射失真:\n%s", out)
	}

	// 平台检索失败：错误回注（不中断 ReAct）。
	sp.err = fmt.Errorf("search down")
	outErr, err := si.(tool.InvokableTool).InvokableRun(context.Background(), `{"keywords":"x"}`)
	if err != nil || !strings.Contains(outErr, "search down") {
		t.Fatalf("检索失败应错误回注: err=%v res=%.120s", err, outErr)
	}
}

// ---------- workcopyDir / SearchCode ----------

func TestWorkcopyDir(t *testing.T) {
	fixture := qaFixtureRepo(t, map[string]string{"README.md": "# fixture\n"})
	e, _, fp := newTestEngine(t, &fakeModel{})
	e.WC = qaPool(t, fixture)
	ctx := context.Background()

	dir, ok := e.workcopyDir(ctx, "gitea", "acme", "app", "7")
	if !ok || dir == "" {
		t.Fatalf("file:// 夹具应就绪工作副本: dir=%q ok=%v", dir, ok)
	}
	if _, err := os.Stat(filepath.Join(dir, "README.md")); err != nil {
		t.Fatalf("工作副本应含夹具文件: %v", err)
	}

	// 无凭证配置 → Ensure 失败静默降级（false 路径 + 留痕）。
	e2, _, _ := newTestEngine(t, &fakeModel{})
	e2.WC = workcopy.NewPool(t.TempDir(), logx.Nop())
	if d, ok := e2.workcopyDir(ctx, "gitea", "acme", "app", "7"); ok || d != "" {
		t.Fatalf("无凭证应降级: dir=%q ok=%v", d, ok)
	}

	// head SHA 缺失（GetCR 空值）→ Ensure 拒绝。
	fp.cr.HeadSHA = ""
	if _, ok := e.workcopyDir(ctx, "gitea", "acme", "app", "7"); ok {
		t.Fatal("无 head SHA 应降级")
	}
}

func TestSearchCodeEnabled(t *testing.T) {
	fixture := qaFixtureRepo(t, map[string]string{
		"serv.go": "package app\n\nfunc limitUnbounded() {}\n",
	})
	e, _, _ := newTestEngine(t, &fakeModel{})
	e.WC = qaPool(t, fixture)
	e.CodeSearch = true
	res, err := e.SearchCode(context.Background(), "gitea", "acme", "app", "7", "limitUnbounded", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res, "limitUnbounded") || !strings.Contains(res, "search_code 检索结果") {
		t.Fatalf("检索应命中夹具内容:\n%s", res)
	}

	// 池未就绪（无凭证）：环境面故障 → error（MCP 侧转工具错误）。
	e2, _, _ := newTestEngine(t, &fakeModel{})
	e2.WC = workcopy.NewPool(t.TempDir(), logx.Nop())
	e2.CodeSearch = true
	if _, err := e2.SearchCode(context.Background(), "gitea", "acme", "app", "7", "x", "", false); err == nil ||
		!strings.Contains(err.Error(), "工作副本不可用") {
		t.Fatalf("工作副本不可用应报错: %v", err)
	}
}

// ---------- SearchSem / Trace / SymContext 启用路径 ----------

// bagEmbedder 确定性词袋 embedder（零网络）：8 维 token 命中向量，归一化。
// 命中由 chunk 文本（path:line + 源码）里的关键词决定——行为可预期。
type bagEmbedder struct{}

var bagTokens = []string{"login", "auth", "token", "cache", "retry", "http", "parse", "session"}

func (bagEmbedder) Dim() int { return len(bagTokens) }

func (bagEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, txt := range texts {
		v := make([]float32, len(bagTokens))
		lower := strings.ToLower(txt)
		norm := 0.0
		for j, tok := range bagTokens {
			if strings.Contains(lower, tok) {
				v[j] = 1
				norm++
			}
		}
		if norm > 0 {
			for j := range v {
				v[j] /= float32(norm)
			}
		}
		out[i] = v
	}
	return out, nil
}

// semEngine 装配语义检索面（夹具仓 + file:// 工作副本池）。
func semEngine(t *testing.T, files map[string]string, e semsearch.Embedder) (*Engine, string) {
	t.Helper()
	fixture := qaFixtureRepo(t, files)
	eng, _, _ := newTestEngine(t, &fakeModel{})
	eng.WC = qaPool(t, fixture)
	eng.SemSearch = semsearch.NewManager(e)
	eng.SemSearchOn = true
	return eng, fixture
}

func TestSearchSemEnabled(t *testing.T) {
	e, _ := semEngine(t, map[string]string{
		"login.go": "package app\n\nfunc loginHandler() { newSession() }\n\nfunc newSession() {}\n",
		"util.go":  "package app\n\nfunc parseConfig() {}\n",
	}, bagEmbedder{})
	ctx := context.Background()
	// 命中路：query 词袋命中 loginHandler/newSession 的 chunk 文本；
	// topK=0 → 缺省 5、topK=99 → 钳到 10（钳制分支顺手钉住）。
	res, err := e.SearchSem(ctx, "gitea", "acme", "app", "7", "login", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res, "search_sem（query=login）命中") || !strings.Contains(res, "login.go") {
		t.Fatalf("应命中 login.go:\n%s", res)
	}
	if !strings.Contains(res, "邻接：") {
		t.Fatalf("符号命中应附图邻接行:\n%s", res)
	}
	if _, err := e.SearchSem(ctx, "gitea", "acme", "app", "7", "login", 99); err != nil {
		t.Fatalf("topK 超上界应钳制不报错: %v", err)
	}

	// 无命中（目录零 Go 文件 → 零 chunk）：提示文本非 error。
	e2, _ := semEngine(t, nil, bagEmbedder{})
	resNone, err := e2.SearchSem(ctx, "gitea", "acme", "app", "7", "login", 5)
	if err != nil || !strings.Contains(resNone, "语义检索无命中") {
		t.Fatalf("无命中应返回提示文本: %q %v", resNone, err)
	}

	// 空参/工作副本不可用守卫。
	if _, err := e.SearchSem(ctx, "gitea", "acme", "app", "7", "  ", 5); err == nil ||
		!strings.Contains(err.Error(), "query 为空") {
		t.Fatalf("空 query 应报错: %v", err)
	}
	e.WC = workcopy.NewPool(t.TempDir(), logx.Nop()) // 无凭证
	if _, err := e.SearchSem(ctx, "gitea", "acme", "app", "7", "login", 5); err == nil ||
		!strings.Contains(err.Error(), "工作副本不可用") {
		t.Fatalf("工作副本不可用应报错: %v", err)
	}
}

func TestTraceEnabled(t *testing.T) {
	// 纯图 Manager（embedder nil）：Trace/SymContext 图面可用，Search 拦截——
	// 与生产「仅图模式」部署形态同构。
	fixture := qaFixtureRepo(t, map[string]string{
		"a.go":   "package a\n\nfunc Alpha() { Beta() }\n",
		"b/b.go": "package b\n\nfunc Beta() {}\n",
	})
	e, _, _ := newTestEngine(t, &fakeModel{})
	e.WC = qaPool(t, fixture)
	e.SemSearch = semsearch.NewManager(nil)
	e.SemSearchOn = true
	ctx := context.Background()

	chain, err := e.Trace(ctx, "gitea", "acme", "app", "7", "Alpha", "Beta")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(chain, "get_trace（Alpha → Beta）") || !strings.Contains(chain, "—calls→") ||
		!strings.Contains(chain, "词法级边") {
		t.Fatalf("调用链渲染失真:\n%s", chain)
	}

	// 不可达（符号不存在）：正常查询结果，提示文本非 error。
	miss, err := e.Trace(ctx, "gitea", "acme", "app", "7", "Alpha", "NoSuchSym")
	if err != nil || !strings.Contains(miss, "未找到 Alpha → NoSuchSym") {
		t.Fatalf("不可达应返回提示文本: %q %v", miss, err)
	}

	// 空参/工作副本不可用守卫。
	if _, err := e.Trace(ctx, "gitea", "acme", "app", "7", " ", "Beta"); err == nil ||
		!strings.Contains(err.Error(), "from/to 为空") {
		t.Fatalf("空 from 应报错: %v", err)
	}
	e.WC = workcopy.NewPool(t.TempDir(), logx.Nop())
	if _, err := e.Trace(ctx, "gitea", "acme", "app", "7", "Alpha", "Beta"); err == nil ||
		!strings.Contains(err.Error(), "工作副本不可用") {
		t.Fatalf("工作副本不可用应报错: %v", err)
	}
}

func TestSymContextEnabled(t *testing.T) {
	// 同名多候选（跨子目录同 bare 名；根目录 decl 键即裸名会 loc 直命中，
	// 故调用方放子目录）→ 合并邻接 + 候选数透出。
	fixture := qaFixtureRepo(t, map[string]string{
		"b/b.go": "package b\n\nfunc Beta() {}\n",
		"d/d.go": "package d\n\nfunc Alpha() { Beta() }\n",
		"c/c.go": "package c\n\nfunc Alpha() {}\n",
	})
	e, _, _ := newTestEngine(t, &fakeModel{})
	e.WC = qaPool(t, fixture)
	e.SemSearch = semsearch.NewManager(nil)
	e.SemSearchOn = true
	ctx := context.Background()

	res, err := e.SymContext(ctx, "gitea", "acme", "app", "7", "Alpha")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res, "get_symbol_context（symbol=Alpha）") ||
		!strings.Contains(res, "定义：c.Alpha (c/c.go:3)") || !strings.Contains(res, "同名候选 2 处") ||
		!strings.Contains(res, "被调（≤8）") || !strings.Contains(res, "b.Beta(b/b.go:3)") {
		t.Fatalf("符号上下文渲染失真:\n%s", res)
	}

	miss, err := e.SymContext(ctx, "gitea", "acme", "app", "7", "NoSuchSym")
	if err != nil || !strings.Contains(miss, "未在索引内解析到符号") {
		t.Fatalf("未解析应返回提示文本: %q %v", miss, err)
	}

	if _, err := e.SymContext(ctx, "gitea", "acme", "app", "7", ""); err == nil ||
		!strings.Contains(err.Error(), "symbol 为空") {
		t.Fatalf("空 symbol 应报错: %v", err)
	}
	e.WC = workcopy.NewPool(t.TempDir(), logx.Nop())
	if _, err := e.SymContext(ctx, "gitea", "acme", "app", "7", "Alpha"); err == nil ||
		!strings.Contains(err.Error(), "工作副本不可用") {
		t.Fatalf("工作副本不可用应报错: %v", err)
	}
}

// ---------- ptcAsk / buildProgramPrompt / generateProgram ----------

const ptcProgOK = "```js\nconst d = await host.get_diff('{}');\nreturn {len: d.length};\n```"
const ptcProgBroken = "const r = await host.get_diff('{}');\nconst leak = {" // 语法错：未闭合
const ptcProgFixed = "```js\nconst d = await host.get_diff('{}');\nreturn {n: 1};\n```"

// ptc 主路径：程序合成（真实 ptc.Run 执行 get_diff 绑定）→ 作答轮带程序取证围栏。
func TestPtcAskHappyPath(t *testing.T) {
	m := &fakeModel{contents: []string{ptcProgOK, "程序化回答：改动已核实"}}
	e, _, _ := newTestEngine(t, m)
	e.PTCEnabled = true
	ans, ok, err := e.ptcAsk(context.Background(), "gitea", "acme", "app", "7", "这个 PR 改了什么", "", "")
	if err != nil || !ok || ans != "程序化回答：改动已核实" {
		t.Fatalf("ptc 主路径失真: ans=%q ok=%v err=%v", ans, ok, err)
	}
	// 作答轮：程序取证结果在数据围栏内。
	sys := m.lastSystemText()
	if !strings.Contains(sys, "程序取证结果") {
		t.Fatalf("作答轮应带取证围栏:\n%.200s", sys)
	}
}

// Answer 集成：PTC 成功即直出（不再走 ReAct 的 8 轮循环），轮次照常落库。
func TestAnswer_PTCDirectHit(t *testing.T) {
	m := &fakeModel{contents: []string{ptcProgOK, "PTC 直答"}}
	e, store, _ := newTestEngine(t, m)
	e.PTCEnabled = true
	ans, err := e.Answer(context.Background(), "gitea", "acme", "app", "7", "acme/app#7", "问题")
	if err != nil || ans != "PTC 直答" {
		t.Fatalf("Answer PTC 失真: %q %v", ans, err)
	}
	sess, err := store.GetQASession(context.Background(), "gitea:acme/app#7", sessionTTL)
	if err != nil || sess == nil {
		t.Fatalf("PTC 轮次应照常落库: %v", err)
	}
}

// ptc 全量回退分支（handled=false → 调用方回退 ReAct，不把 ptc 失败当问答失败）。
func TestPtcAskFallbacks(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name     string
		contents []string
		errs     []error
	}{
		{"合成失败", nil, []error{fmt.Errorf("api down")}},
		{"空程序", []string{"这不是程序，没有围栏也没有 host."}, nil},
		{"语法错且回喂后放弃（修复轮与原程序相同）", []string{ptcProgBroken, ptcProgBroken}, nil},
		{"预算截停（budget_calls 不可修复）", []string{
			"```js\nfor (let i = 0; i < 99; i++) { await host.get_file('{\"path\":\"main.go\"}'); }\nreturn 1;\n```",
		}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := &fakeModel{contents: c.contents, errs: c.errs}
			e, _, _ := newTestEngine(t, m)
			ans, ok, err := e.ptcAsk(ctx, "gitea", "acme", "app", "7", "q", "", "")
			if err != nil || ok || ans != "" {
				t.Fatalf("应回退: ans=%q ok=%v err=%v", ans, ok, err)
			}
		})
	}

	// 修复回喂成功：坏程序 → 诊断回喂 → 修好 → 作答。
	m := &fakeModel{contents: []string{ptcProgBroken, ptcProgFixed, "修复后回答"}}
	e, _, _ := newTestEngine(t, m)
	ans, ok, err := e.ptcAsk(ctx, "gitea", "acme", "app", "7", "q", "", "")
	if err != nil || !ok || ans != "修复后回答" {
		t.Fatalf("修复回喂后应成功: ans=%q ok=%v err=%v", ans, ok, err)
	}

	// 修复成功但作答轮模型失败：同样回退。
	m2 := &fakeModel{contents: []string{ptcProgBroken, ptcProgFixed}}
	m2.errs = []error{nil, nil, fmt.Errorf("answer api down")} // 前两次放行（合成/修复），第三次拒
	e2, _, _ := newTestEngine(t, m2)
	if ans, ok, err := e2.ptcAsk(ctx, "gitea", "acme", "app", "7", "q", "", ""); err != nil || ok || ans != "" {
		t.Fatalf("作答失败应回退: ans=%q ok=%v err=%v", ans, ok, err)
	}
}

// buildProgramPrompt：工具清单（净化名 + 首行描述）、契约段、报告锚围栏可选注入。
func TestBuildProgramPrompt(t *testing.T) {
	tools := []tool.BaseTool{
		&fakeInvokableTool{name: "get-file", desc: "读文件首行\n第二行不该出现"},
	}
	sys := buildProgramPrompt("问题", "历史", "报告正文", "策略段", tools)
	for _, want := range []string{
		"host.get_file", "读文件首行", "策略段", "程序契约", "调用预算 16",
		"最近审查报告（数据）", "报告正文",
	} {
		if !strings.Contains(sys, want) {
			t.Fatalf("缺 %q:\n%s", want, sys)
		}
	}
	if strings.Contains(sys, "第二行不该出现") {
		t.Fatal("工具描述应只取首行")
	}
	// 无报告锚：不注入围栏。
	bare := buildProgramPrompt("q", "", "", "", tools)
	if strings.Contains(bare, "最近审查报告") {
		t.Fatalf("无锚不应注入围栏:\n%s", bare)
	}
}

// generateProgram：围栏提取、修复回喂形态（user message 带诊断）、模型失败上抛。
func TestGenerateProgram(t *testing.T) {
	m := &fakeModel{contents: []string{"```js\nreturn 42;\n```"}}
	e := &Engine{Model: m, Log: testNopLogger{}}
	prog, err := e.generateProgram(context.Background(), "sys", "问题", "")
	if err != nil || prog != "return 42;" {
		t.Fatalf("程序提取失真: %q %v", prog, err)
	}
	if !strings.Contains(m.lastUserText(), "问题") {
		t.Fatalf("user 应为原问题:\n%s", m.lastUserText())
	}

	// 修复回喂形态：诊断 + 原问题进 user message。
	if _, err := e.generateProgram(context.Background(), "sys", "问题", "第 3 行括号不配对"); err != nil {
		t.Fatal(err)
	}
	u := m.lastUserText()
	if !strings.Contains(u, "诊断信息") || !strings.Contains(u, "第 3 行括号不配对") || !strings.Contains(u, "原问题") {
		t.Fatalf("回喂形态失真:\n%s", u)
	}

	// 模型失败上抛（调用方回退 ReAct）。
	m2 := &fakeModel{errs: []error{fmt.Errorf("boom")}}
	e2 := &Engine{Model: m2, Log: testNopLogger{}}
	if _, err := e2.generateProgram(context.Background(), "sys", "q", ""); err == nil {
		t.Fatal("模型失败应上抛")
	}
}

// ptc 诊断回喂形态契约：喂给修复轮的 Describe 产物必须模型可行动（点明缺 return）。
// 此处钉 ptcAsk 内层回喂的输入前提（渲染单源在 harness 侧已钉）。
func TestPtcRepairFeedbackContract(t *testing.T) {
	res := ptc.Run(context.Background(), "const x = 1;", []ptc.Binding{{
		Name: "noop", Call: func(context.Context, string) (string, error) { return "{}", nil },
	}}, ptc.Options{Timeout: ptcTimeout, MaxCalls: 4, MaxBytes: 4096, AllowUnmanaged: true})
	if !res.OK || res.Value != "null" || !programFixable(res) {
		t.Fatalf("缺 return 程序应 OK/null/可修复: %+v", res)
	}
	if fb := res.Describe(); !strings.Contains(fb, "return") {
		t.Fatalf("回喂诊断应点明缺 return: %q", fb)
	}
}

// ---------- persistTurn 乐观锁冲突重放 ----------

// CAS 冲突重放：本回合读到的 expect 已过期 → 重读最新历史把本轮追加其后，
// 外部并发轮次与本轮都不丢。
func TestPersistTurn_CASReplay(t *testing.T) {
	e, store, _ := newTestEngine(t, &fakeModel{})
	ctx := context.Background()
	key, prRef := "gitea:acme/app#7", "acme/app#7"
	// 先有会话（prev.sess 非 nil 才启用 CAS 校验）再读 prev。
	seed := []storage.QATurn{{Question: "seed 问", Answer: "seed 答", At: "t"}}
	seedHJ, _ := json.Marshal(seed)
	if err := store.SaveQASession(ctx, &storage.QASession{
		SessionKey: key, HistoryJSON: string(seedHJ), Summary: "seed 摘要",
	}); err != nil {
		t.Fatal(err)
	}
	prev := e.loadQASession(ctx, key, prRef)
	if prev.sess == nil {
		t.Fatal("应读到既有会话")
	}

	// 外部并发写入（bump UpdatedAt → 本轮 CAS 校验必失败）。
	extTurns := []storage.QATurn{{Question: "外部并发问", Answer: "外部并发答", At: "t"}}
	extHJ, _ := json.Marshal(extTurns)
	if err := store.SaveQASession(ctx, &storage.QASession{
		SessionKey: key, HistoryJSON: string(extHJ), Summary: "外部摘要",
	}); err != nil {
		t.Fatal(err)
	}

	newTurns := appendTurn(nil, "本轮问", "本轮答")
	e.persistTurn(ctx, key, prRef, prev, "本轮问", "本轮答", newTurns, "新摘要")

	sess, err := store.GetQASession(ctx, key, sessionTTL)
	if err != nil || sess == nil {
		t.Fatalf("重放后应可读回: %v", err)
	}
	var got []storage.QATurn
	if err := json.Unmarshal([]byte(sess.HistoryJSON), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Question != "外部并发问" || got[1].Question != "本轮问" {
		t.Fatalf("重放应保外部轮并追加本轮: %+v", got)
	}
	if sess.Summary != "新摘要" {
		t.Fatalf("重放应落新摘要: %q", sess.Summary)
	}
}

// CAS 冲突 + 重读坏 JSON：历史已损坏 → 以本轮为起点覆盖落库（留痕不 panic），
// 会话不丢本轮、外部坏数据不回流。
func TestPersistTurn_ReplayCorruptHistory(t *testing.T) {
	e, store, _ := newTestEngine(t, &fakeModel{})
	ctx := context.Background()
	key, prRef := "gitea:acme/app#7", "acme/app#7"
	if err := store.SaveQASession(ctx, &storage.QASession{
		SessionKey: key, HistoryJSON: `[]`,
	}); err != nil {
		t.Fatal(err)
	}
	prev := e.loadQASession(ctx, key, prRef)

	if err := store.SaveQASession(ctx, &storage.QASession{
		SessionKey: key, HistoryJSON: "{not-json",
	}); err != nil {
		t.Fatal(err)
	}
	newTurns := appendTurn(nil, "q", "a")
	e.persistTurn(ctx, key, prRef, prev, "q", "a", newTurns, "")

	sess, err := store.GetQASession(ctx, key, sessionTTL)
	if err != nil || sess == nil {
		t.Fatalf("应落库: %v", err)
	}
	var got []storage.QATurn
	if err := json.Unmarshal([]byte(sess.HistoryJSON), &got); err != nil {
		t.Fatalf("落库历史应恢复合法: %v (%s)", err, sess.HistoryJSON)
	}
	if len(got) != 1 || got[0].Question != "q" {
		t.Fatalf("坏历史应以本轮为起点重建: %+v", got)
	}
}

// logx.Nop 编译期对齐（qa 包测试经 testNopLogger 满足 NewSDKCtxStubbed 签名；
// 此处防 logx 依赖被误删后 helper 静默漂移）。
var _ logx.Logger = testNopLogger{}
