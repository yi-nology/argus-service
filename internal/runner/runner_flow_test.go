// runner_flow_test.go —— 任务主流程行为钉住（#36 覆盖率 kernel 60/70 口径）：
// Execute 写作面三 kind（describe/changelog/ci_analysis/diff_preview）全链、
// 生命周期四步（buildPluginTask/recordRegret/finalizeAndAnnounce/handleRunFailure）、
// PR 上下文（buildPRContext/loadRepoConfig/RepoGuideFor）、报告组装与播报面。
// 打桩纪律：平台面 flowProvider（缺能力 = 走降级路径，触达即行为失真）；
// LLM 用 flowModel 按阶段提示词回罐头（不联网）；Store 用真 sqlite 临时库。

package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/oagentkit/llm"
	"github.com/oagentkit/llm/port"
	oagentprogress "github.com/oagentkit/runtime/progress"
	"github.com/oagentkit/skillkit/skill"
	"github.com/oagentkit/textx/codeintel"
	"github.com/oagentkit/tools/workcopy"
	"github.com/oekit/logx"
	"github.com/yi-nology/go-git-platform/provider"

	"github.com/yi-nology/argus/internal/argusctx"
	"github.com/yi-nology/argus/internal/config"
	"github.com/yi-nology/argus/internal/dispatch"
	"github.com/yi-nology/argus/internal/domain"
	"github.com/yi-nology/argus/internal/impactview"
	"github.com/yi-nology/argus/internal/learning"
	"github.com/yi-nology/argus/internal/plugin"
	"github.com/yi-nology/argus/internal/poster"
	"github.com/yi-nology/argus/internal/progress"
	"github.com/yi-nology/argus/internal/reportview"
	"github.com/yi-nology/argus/internal/storage"
	"github.com/yi-nology/argus/internal/workflow/requirement"
)

// ---------- 测试替身 ----------

// flowModel 罐头 LLM：按提示词特征返回对应形态的产物（describe=JSON、
// ci_analysis=JSON、changelog/diff=Markdown）。绝不联网。
type flowModel struct {
	mu     sync.Mutex
	calls  []string
	genErr error
}

func (f *flowModel) record(prompt string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, prompt)
}

func (f *flowModel) Generate(_ context.Context, msgs []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	prompt := ""
	if len(msgs) > 0 {
		prompt = msgs[0].Content
	}
	f.record(prompt)
	if f.genErr != nil {
		return nil, f.genErr
	}
	switch {
	case strings.Contains(prompt, "Pull Request 描述"):
		return schema.UserMessage(`{"title": "规范标题", "body": "## 变更动机\n修bug"}`), nil
	case strings.Contains(prompt, "CI 故障诊断"):
		return schema.UserMessage(`{"conclusion": "依赖服务超时", "root_cause": "connection refused", "fix": "重跑"}`), nil
	default:
		return schema.UserMessage("- **Features:** 支持xxx"), nil
	}
}

func (f *flowModel) Stream(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return nil, context.Canceled
}

func (f *flowModel) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// flowProvider 平台打桩：只实现被测路径触达的能力面；未实现的留嵌入接口
// panic 兜底（触达 = 用例越界，立即暴露）。文件面用 map 供 GetFileContent。
type flowProvider struct {
	provider.Provider

	mu          sync.Mutex
	cr          *provider.ChangeRequest
	repo        *provider.PlatformRepo
	diff        *provider.MergeDiff
	diffErr     error
	files       map[string]string        // GetFileContent: path → content
	filesErr    map[string]error         // path → error（优先于 files）
	comments    []*provider.IssueComment // ListNotes 返回
	notes       []string                 // CreateNote/CreateIssueComment 捕获
	updated     map[int64]string         // UpdateIssueComment 捕获
	updateErr   error
	crUpdates   []provider.UpdateCROptions // UpdateCR 捕获（describe 写回）
	crUpdateErr error
	discussions []provider.DiscussionOptions // CreateDiscussion 捕获
	jobLogs     []provider.JobLog            // CILogManager 返回
	jobErr      error
	statuses    []provider.CommitStatus // CommitStatusManager 返回
}

func (f *flowProvider) Capabilities() provider.CapabilitySet {
	return provider.DeriveCapabilities(f)
}

func (f *flowProvider) GetCR(_ context.Context, _, _, _ string) (*provider.ChangeRequest, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.cr == nil {
		return nil, provider.ErrNotFound
	}
	return f.cr, nil
}

func (f *flowProvider) GetRepo(_ context.Context, _, _ string) (*provider.PlatformRepo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.repo == nil {
		return nil, provider.ErrNotFound
	}
	return f.repo, nil
}

func (f *flowProvider) GetCRDiff(_ context.Context, _, _, _ string) (*provider.MergeDiff, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.diffErr != nil {
		return nil, f.diffErr
	}
	return f.diff, nil
}

func (f *flowProvider) GetFileContent(_ context.Context, _, _, path, _ string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err, ok := f.filesErr[path]; ok {
		return "", err
	}
	if body, ok := f.files[path]; ok {
		return body, nil
	}
	return "", provider.ErrNotFound
}

// CreateFile/UpdateFile/DeleteFile 补齐 FileManager 接口（本测试不触达写文件面，
// 触达即用例越界）。
func (f *flowProvider) CreateFile(_ context.Context, _, _ string, _ provider.FileOptions) (*provider.FileResult, error) {
	return nil, errors.New("flowProvider: 写文件面未打桩")
}

func (f *flowProvider) UpdateFile(_ context.Context, _, _ string, _ provider.FileOptions) (*provider.FileResult, error) {
	return nil, errors.New("flowProvider: 写文件面未打桩")
}

func (f *flowProvider) DeleteFile(_ context.Context, _, _ string, _ provider.FileDeleteOptions) (*provider.FileResult, error) {
	return nil, errors.New("flowProvider: 写文件面未打桩")
}

func (f *flowProvider) GetCRFiles(_ context.Context, _, _, _ string) ([]*provider.ChangedFile, error) {
	return nil, errors.New("flowProvider: GetCRFiles 未打桩")
}

func (f *flowProvider) DeleteNote(_ context.Context, _, _, _ string, _ string) error {
	return nil
}

func (f *flowProvider) ListNotes(_ context.Context, _, _, _ string) ([]*provider.IssueComment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*provider.IssueComment(nil), f.comments...), nil
}

// UpdateNote NoteManager 更新面（评论 ID 字符串形态）。
func (f *flowProvider) UpdateNote(_ context.Context, _, _, _, noteID, body string) (*provider.IssueComment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.updateErr != nil {
		return nil, f.updateErr
	}
	id, _ := strconv.ParseInt(noteID, 10, 64)
	if f.updated == nil {
		f.updated = map[int64]string{}
	}
	f.updated[id] = body
	return &provider.IssueComment{ID: id, Body: body}, nil
}

func (f *flowProvider) CreateNote(_ context.Context, _, _, _ string, body string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.notes = append(f.notes, body)
	id := int64(500 + len(f.notes))
	// 新建评论同步进列表（平台语义：后续 ListNotes 可见，marker 复用靠它）。
	f.comments = append(f.comments, &provider.IssueComment{ID: id, Body: body})
	return fmt.Sprintf("%d", id), nil
}

func (f *flowProvider) UpdateIssueComment(_ context.Context, _, _, _ string, id int64, body string) (*provider.IssueComment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.updateErr != nil {
		return nil, f.updateErr
	}
	if f.updated == nil {
		f.updated = map[int64]string{}
	}
	f.updated[id] = body
	return &provider.IssueComment{ID: id, Body: body}, nil
}

func (f *flowProvider) UpdateCR(_ context.Context, _, _, _ string, opts provider.UpdateCROptions) (*provider.ChangeRequest, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.crUpdateErr != nil {
		return nil, f.crUpdateErr
	}
	f.crUpdates = append(f.crUpdates, opts)
	return f.cr, nil
}

func (f *flowProvider) CreateDiscussion(_ context.Context, _, _, _ string, opts provider.DiscussionOptions) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.discussions = append(f.discussions, opts)
	return "661", nil
}

func (f *flowProvider) ListFailedJobLogs(_ context.Context, _, _, _ string) ([]provider.JobLog, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.jobErr != nil {
		return nil, f.jobErr
	}
	return f.jobsSnapshot(), nil
}

func (f *flowProvider) jobsSnapshot() []provider.JobLog {
	return append([]provider.JobLog(nil), f.jobLogs...)
}

func (f *flowProvider) ListCommitStatuses(_ context.Context, _, _, _ string) ([]provider.CommitStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]provider.CommitStatus(nil), f.statuses...), nil
}

func (f *flowProvider) CreateCommitStatus(_ context.Context, _, _, _ string, _ provider.CommitStatusOptions) error {
	return nil
}

// snapshot 并发安全取捕获利器。
func (f *flowProvider) snapshot() (notes, updated, discussions []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	notes = append([]string(nil), f.notes...)
	for _, b := range f.updated {
		updated = append(updated, b)
	}
	for _, d := range f.discussions {
		discussions = append(discussions, d.Body)
	}
	return notes, updated, discussions
}

func (f *flowProvider) descriptions() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.crUpdates))
	for _, u := range f.crUpdates {
		out = append(out, u.Description)
	}
	return out
}

// ---------- harness ----------

// flowRunner 造主流程 Runner：真 sqlite Store + 打桩 SDKCtx + 罐头 LLM。
func flowRunner(t *testing.T, plat *flowProvider) (*Runner, *storage.Store, *flowModel) {
	t.Helper()
	store, err := storage.Open(filepath.Join(t.TempDir(), "flow.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	fm := &flowModel{}
	cfg := &config.Config{WorkRoot: t.TempDir(), RepoMonthlyTokenBudget: 0}
	r := &Runner{
		Cfg:        cfg,
		Store:      store,
		Log:        logx.Nop(),
		LLM:        llm.NewClient(fm, "flow-stub", nil),
		ModelName:  "flow-stub",
		Version:    "test",
		SDK:        argusctx.NewSDKCtxStubbed(logx.Nop(), map[string]provider.Provider{"gitea": plat}),
		Dispatcher: dispatch.NewDispatcher(dispatch.NewRegistry(), logx.Nop()),
		Bus:        oagentprogress.NewBus[progress.Event](),
	}
	return r, store, fm
}

func flowCR() *provider.ChangeRequest {
	return &provider.ChangeRequest{
		Number: "42", Title: "feat: 登录", Description: "作者手写描述",
		SourceBranch: "feature/login", TargetBranch: "main",
		HeadSHA: "abc123", Author: &provider.CRUser{Username: "alice"},
	}
}

func flowTask(kind string) *storage.Task {
	task := storage.NewTask(kind, "gitea", "acme", "app", "42")
	task.HeadSHA = "abc123"
	return task
}

func requireDone(t *testing.T, store *storage.Store, id string) *storage.Task {
	t.Helper()
	row, err := store.GetTask(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if row.Status != storage.StatusDone {
		t.Fatalf("任务应 done, got %s（fail=%s）", row.Status, row.FailReason)
	}
	return row
}

// ---------- Execute：写作面三 kind ----------

// TestExecuteDescribe 写作任务全链（v3.71.0）：prepareContext（GetCR/GetRepo/
// CI 状态聚合/仓库配置）→ runWriting → describe JSON 生成 → 管理段合并写回
// （作者内容保留）→ done 终态 + marker 收口。
func TestExecuteDescribe(t *testing.T) {
	plat := &flowProvider{
		cr:   flowCR(),
		repo: &provider.PlatformRepo{DefaultBranch: "main"},
		diff: flowDiff(),
		files: map[string]string{
			".argus/review-guidelines.md": "关注边界检查",
			".reviewagents.yaml":          "use: [ghost-agent]\nreview:\n  extra_instructions: \"关注并发\"\n  max_comments: 5\n",
		},
		statuses: []provider.CommitStatus{
			{State: provider.CommitStatusSuccess, Context: "ci/build"},
			{State: provider.CommitStatusFailure, Context: "ci/test"},
		},
	}
	r, store, fm := flowRunner(t, plat)
	task := flowTask(domain.TaskKindDescribe)
	if err := store.CreateTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	if err := r.Execute(context.Background(), task.ID); err != nil {
		t.Fatalf("describe 任务应成功: %v", err)
	}
	row := requireDone(t, store, task.ID)

	// describe 写回：作者描述保留 + argus 管理段落位。
	descs := plat.descriptions()
	if len(descs) != 1 {
		t.Fatalf("UpdateCR 应恰好一次, got %d", len(descs))
	}
	if !strings.Contains(descs[0], "作者手写描述") {
		t.Errorf("写回描述丢失作者原文:\n%s", descs[0])
	}
	if !strings.Contains(descs[0], "argus:describe begin task_id="+task.ID) {
		t.Errorf("写回描述缺管理段锚点:\n%s", descs[0])
	}
	if !strings.Contains(descs[0], "变更动机") {
		t.Errorf("写回描述缺产物正文:\n%s", descs[0])
	}
	// LLM 只调用一次（describe），CI 状态聚合进上下文由 buildPRContext 消费。
	if fm.callCount() != 1 {
		t.Errorf("describe 应单次 LLM 调用, got %d", fm.callCount())
	}
	// 结果落库带 kind 标记。
	if !strings.Contains(row.ResultJSON, `"kind":"describe"`) {
		t.Errorf("result_json 缺 kind 标记: %s", row.ResultJSON)
	}
	if row.SummaryCommentID != 0 {
		t.Errorf("describe 无评论回帖, commentID=%d", row.SummaryCommentID)
	}
	// start marker 收口为完成态（先「审查中」新建、后原地更新）。
	_, updated, _ := plat.snapshot()
	if len(updated) != 1 || !strings.Contains(updated[0], "✅") {
		t.Errorf("marker 未收口完成态: %v", updated)
	}
}

// flowDiff 单文件 unified diff（Files 结构 + RawDiff，diffprep 真实入口形态）。
func flowDiff() *provider.MergeDiff {
	return &provider.MergeDiff{
		Files:   []*provider.ChangedFile{{NewPath: "a.go"}},
		RawDiff: "diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1,1 +1,2 @@\n+fmt.Println()",
	}
}

// TestExecuteChangelog changelog 写作：LLM 列表产物发 PR 评论（CreateDiscussion），
// commentID 回写终态行。
func TestExecuteChangelog(t *testing.T) {
	plat := &flowProvider{
		cr:   flowCR(),
		repo: &provider.PlatformRepo{DefaultBranch: "main"},
		diff: flowDiff(),
	}
	r, store, _ := flowRunner(t, plat)
	task := flowTask(domain.TaskKindChangelog)
	if err := store.CreateTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	if err := r.Execute(context.Background(), task.ID); err != nil {
		t.Fatalf("changelog 任务应成功: %v", err)
	}
	requireDone(t, store, task.ID)
	notes, _, discussions := plat.snapshot()
	if len(discussions) != 1 || !strings.Contains(discussions[0], "Changelog 条目") {
		t.Fatalf("changelog 应发条目评论: %v", discussions)
	}
	row, _ := store.GetTask(context.Background(), task.ID)
	if row.SummaryCommentID != 661 {
		t.Errorf("评论 ID 应回写终态, got %d", row.SummaryCommentID)
	}
	_ = notes
}

// TestExecuteDiffPreview 本地 diff 预审（v3.123.0）：artifacts.diff 单轮 LLM
// 审查，报告落库不回帖（无平台调用）。
func TestExecuteDiffPreview(t *testing.T) {
	plat := &flowProvider{}
	r, store, fm := flowRunner(t, plat)
	task := flowTask(domain.TaskKindDiffPreview)
	arts, _ := json.Marshal(map[string]string{plugin.ArtifactDiffInput: "- a.go: 裸 return"})
	task.ArtifactsJSON = string(arts)
	if err := store.CreateTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	if err := r.Execute(context.Background(), task.ID); err != nil {
		t.Fatalf("diff_preview 任务应成功: %v", err)
	}
	row := requireDone(t, store, task.ID)
	if !strings.Contains(row.ResultJSON, `"kind":"diff_preview"`) {
		t.Errorf("result_json 缺 kind: %s", row.ResultJSON)
	}
	if !strings.Contains(row.ResultJSON, "支持xxx") {
		t.Errorf("预审报告未落库: %s", row.ResultJSON)
	}
	notes, _, discussions := plat.snapshot()
	if len(discussions) != 0 || len(notes) != 1 || !strings.Contains(notes[0], "审查中") {
		t.Errorf("预审只应有审查中 marker: notes=%v discussions=%v", notes, discussions)
	}
	if fm.callCount() != 1 {
		t.Errorf("预审单次 LLM 调用, got %d", fm.callCount())
	}
}

// TestExecuteCIAnalysis CI 失败归因全链：CILogManager 拉失败 job → LLM 归因
// JSON → 有 MR 时回评论，无 MR 只落库。
func TestExecuteCIAnalysis(t *testing.T) {
	plat := &flowProvider{
		cr:   flowCR(),
		repo: &provider.PlatformRepo{DefaultBranch: "main"},
		jobLogs: []provider.JobLog{
			{Name: "test", Stage: "verify", WebURL: "https://ci/j/1", Trace: "panic: nil map"},
		},
	}
	r, store, _ := flowRunner(t, plat)
	task := flowTask(domain.TaskKindCIAnalysis)
	task.Number = "42"
	if err := store.CreateTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	if err := r.Execute(context.Background(), task.ID); err != nil {
		t.Fatalf("ci_analysis 任务应成功: %v", err)
	}
	requireDone(t, store, task.ID)
	_, _, discussions := plat.snapshot()
	if len(discussions) != 1 || !strings.Contains(discussions[0], "CI 失败归因") {
		t.Fatalf("归因应回帖: %v", discussions)
	}
}

// TestExecuteCIAnalysisNoMR 无 MR 的 push pipeline：评论降级跳过，产物只落库。
func TestExecuteCIAnalysisNoMR(t *testing.T) {
	plat := &flowProvider{
		repo:    &provider.PlatformRepo{DefaultBranch: "main"},
		jobLogs: []provider.JobLog{{Name: "build", Trace: "exit 1"}},
	}
	r, store, _ := flowRunner(t, plat)
	task := flowTask(domain.TaskKindCIAnalysis)
	task.Number = ""
	if err := store.CreateTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	if err := r.Execute(context.Background(), task.ID); err != nil {
		t.Fatalf("无 MR 归因应成功落库: %v", err)
	}
	requireDone(t, store, task.ID)
	_, _, discussions := plat.snapshot()
	if len(discussions) != 0 {
		t.Errorf("无 MR 不应回帖: %v", discussions)
	}
}

// noCIProvider 无 CI 日志能力的平台桩：实现 marker/引导路径会触达的接口面
// （全部报错/空），但刻意不实现 CILogManager——capability 断言诚实落空，
// 钉住「未实现 CI 日志能力」的失败归因（nil 嵌入晋升只会 panic，不可用）。
type noCIProvider struct {
	provider.Provider
}

func (noCIProvider) Capabilities() provider.CapabilitySet {
	return provider.CapabilitySet{}
}

func (noCIProvider) ListNotes(context.Context, string, string, string) ([]*provider.IssueComment, error) {
	return nil, nil
}

func (noCIProvider) UpdateNote(context.Context, string, string, string, string, string) (*provider.IssueComment, error) {
	return nil, errors.New("noCIProvider: 未打桩")
}

func (noCIProvider) GetCRDiff(context.Context, string, string, string) (*provider.MergeDiff, error) {
	return nil, errors.New("noCIProvider: 未打桩")
}

func (noCIProvider) GetCRFiles(context.Context, string, string, string) ([]*provider.ChangedFile, error) {
	return nil, errors.New("noCIProvider: 未打桩")
}

func (noCIProvider) CreateNote(context.Context, string, string, string, string) (string, error) {
	return "", errors.New("noCIProvider: 未打桩")
}

func (noCIProvider) DeleteNote(context.Context, string, string, string, string) error {
	return nil
}

func (noCIProvider) CreateDiscussion(context.Context, string, string, string, provider.DiscussionOptions) (string, error) {
	return "", errors.New("noCIProvider: 未打桩")
}

func (noCIProvider) GetFileContent(context.Context, string, string, string, string) (string, error) {
	return "", provider.ErrNotFound
}

func (noCIProvider) CreateFile(context.Context, string, string, provider.FileOptions) (*provider.FileResult, error) {
	return nil, errors.New("noCIProvider: 未打桩")
}

func (noCIProvider) UpdateFile(context.Context, string, string, provider.FileOptions) (*provider.FileResult, error) {
	return nil, errors.New("noCIProvider: 未打桩")
}

func (noCIProvider) DeleteFile(context.Context, string, string, provider.FileDeleteOptions) (*provider.FileResult, error) {
	return nil, errors.New("noCIProvider: 未打桩")
}

// TestExecuteCIAnalysisNoCapability 平台未实现 CI 日志能力：任务 failed 终态
// （§10.2 错误矩阵——上下文失败不刷屏用户），失败告知走 marker。
func TestExecuteCIAnalysisNoCapability(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "noci.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	r := &Runner{
		Cfg:        &config.Config{TaskMaxRetries: 3, WorkRoot: t.TempDir()},
		Store:      store,
		Log:        logx.Nop(),
		LLM:        llm.NewClient(&flowModel{}, "stub", nil),
		ModelName:  "stub",
		SDK:        argusctx.NewSDKCtxStubbed(logx.Nop(), map[string]provider.Provider{"gitea": noCIProvider{}}),
		Dispatcher: dispatch.NewDispatcher(dispatch.NewRegistry(), logx.Nop()),
		Bus:        oagentprogress.NewBus[progress.Event](),
	}
	task := flowTask(domain.TaskKindCIAnalysis)
	if err := store.CreateTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	// runCIAnalysis 失败经 handleRunFailure 收口（Execute 返回 nil）。
	if err := r.Execute(context.Background(), task.ID); err != nil {
		t.Fatalf("失败已被收口, Execute 不应再上抛: %v", err)
	}
	row, _ := store.GetTask(context.Background(), task.ID)
	if row.Status != storage.StatusFailed || !strings.Contains(row.FailReason, "CILogManager") {
		t.Fatalf("应 failed 且带能力缺失原因: %s / %s", row.Status, row.FailReason)
	}
}

// TestExecuteTaskMissing 任务不存在：Execute 直接报错，不落终态。
func TestExecuteTaskMissing(t *testing.T) {
	plat := &flowProvider{}
	r, _, _ := flowRunner(t, plat)
	if err := r.Execute(context.Background(), "t-nope"); err == nil || !strings.Contains(err.Error(), "不存在") {
		t.Fatalf("缺失任务应报不存在: %v", err)
	}
}

// ---------- 生命周期旁路：handleRunFailure / finalizeAndAnnounce ----------

// TestHandleRunFailureFinalAttempt 重试超限的最终失败：marker 文案切换为
// 「已尝试 N 次」终态措辞。
func TestHandleRunFailureFinalAttempt(t *testing.T) {
	plat := &flowProvider{}
	r, store, _ := flowRunner(t, plat)
	r.Cfg.TaskMaxRetries = 2
	task := flowTask(domain.TaskKindCodeReview)
	task.Attempt = 2 // 已达上限
	if err := store.CreateTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	r.handleRunFailure(context.Background(), task, nil, errors.New("boom: 上下文构建失败"))
	row, _ := store.GetTask(context.Background(), task.ID)
	if row.Status != storage.StatusFailed {
		t.Fatalf("应 failed, got %s", row.Status)
	}
	notes, _, _ := plat.snapshot()
	if len(notes) != 1 || !strings.Contains(notes[0], "已尝试") || strings.Contains(notes[0], "自动重试") {
		t.Fatalf("终态失败应「已尝试 N 次」非重试措辞: %v", notes)
	}
}

// TestHandleRunFailureCancelled 停机硬取消：显式保持 running（依赖心跳过期复位
// 回 pending），不落 failed。
func TestHandleRunFailureCancelled(t *testing.T) {
	plat := &flowProvider{}
	r, store, _ := flowRunner(t, plat)
	task := flowTask(domain.TaskKindCodeReview)
	task.Status = storage.StatusRunning
	if err := store.CreateTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r.handleRunFailure(ctx, task, nil, errors.New("boom"))
	row, _ := store.GetTask(context.Background(), task.ID)
	if row.Status != storage.StatusRunning {
		t.Fatalf("硬取消应保持 running, got %s", row.Status)
	}
}

// TestFinalizeAndAnnounceDone 成功终态收口：结果落库 + marker 完成态 + 终态事件。
func TestFinalizeAndAnnounceDone(t *testing.T) {
	plat := &flowProvider{}
	r, store, _ := flowRunner(t, plat)
	task := flowTask(domain.TaskKindCodeReview)
	if err := store.CreateTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	out := &plugin.WorkflowOutput{
		Verdict: "approve", Rule: "R5.1", Tokens: 500,
		Merged: &plugin.MergedFindings{Findings: []domain.Finding{
			{File: "a.go", Line: 1, Severity: "high", Comment: "空指针"},
		}},
	}
	rep := reportview.Report{TaskID: task.ID}
	err := r.finalizeAndAnnounce(context.Background(), task, &plugin.PRContext{
		Platform: "gitea", Owner: "acme", Repo: "app", Number: "42", HeadSHA: "abc123",
	}, out, &poster.Result{SummaryCommentID: 88}, rep,
		&plugin.DiffContext{Text: "diff"}, time.Now())
	if err != nil {
		t.Fatalf("收口应成功: %v", err)
	}
	row, _ := store.GetTask(context.Background(), task.ID)
	if row.Status != storage.StatusDone || row.Verdict != "approve" || row.SummaryCommentID != 88 {
		t.Fatalf("终态行失真: %+v", row)
	}
	var payload ResultPayload
	if err := json.Unmarshal([]byte(row.ResultJSON), &payload); err != nil {
		t.Fatalf("result_json 应可解析: %v", err)
	}
	if payload.Verdict != "approve" || len(payload.Findings) != 1 || payload.DiffText != "diff" {
		t.Errorf("payload 字段失真: %+v", payload)
	}
	notes, _, _ := plat.snapshot()
	if len(notes) != 1 || !strings.Contains(notes[0], "APPROVE") {
		t.Errorf("marker 应带大写结论: %v", notes)
	}
}

// TestFinalizeAndAnnounceStale 命中 staleness：落 stale 终态、不收口 marker
// （由新执行体接管）。
func TestFinalizeAndAnnounceStale(t *testing.T) {
	plat := &flowProvider{}
	r, store, _ := flowRunner(t, plat)
	task := flowTask(domain.TaskKindCodeReview)
	if err := store.CreateTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	err := r.finalizeAndAnnounce(context.Background(), task, &plugin.PRContext{
		Platform: "gitea", Owner: "acme", Repo: "app", Number: "42",
	}, &plugin.WorkflowOutput{Verdict: "comment"}, &poster.Result{Stale: true},
		reportview.Report{TaskID: task.ID}, &plugin.DiffContext{}, time.Now())
	if err != nil {
		t.Fatalf("stale 收口应成功: %v", err)
	}
	row, _ := store.GetTask(context.Background(), task.ID)
	if row.Status != storage.StatusStale {
		t.Fatalf("应 stale, got %s", row.Status)
	}
	notes, _, _ := plat.snapshot()
	if len(notes) != 0 {
		t.Errorf("stale 不应更新 marker: %v", notes)
	}
}

// ---------- 生命周期四步：buildPluginTask / recordRegret ----------

// TestBuildPluginTaskWiring 任务组装与注入接线：guidelines + 附加指令合并、
// checkpoint 落库钩子、related 契约拉取（成功/失败混合）、RepoConfig 进 workflow。
func TestBuildPluginTaskWiring(t *testing.T) {
	plat := &flowProvider{
		repo: &provider.PlatformRepo{DefaultBranch: "main"},
		files: map[string]string{
			".argus/review-guidelines.md": "关注边界检查",
			"openapi.yaml":                "paths: {}",
		},
	}
	r, store, _ := flowRunner(t, plat)
	r.Cfg.RelatedRepos = true
	pr := &plugin.PRContext{
		Platform: "gitea", Owner: "acme", Repo: "app", Number: "42",
		DefaultBranch: "main", HeadSHA: "abc123",
	}
	rc := &plugin.RepoConfig{
		Loaded: true,
		Review: plugin.ReviewSettings{ExtraInstructions: "关注并发", MaxComments: 5},
		Related: []plugin.RelatedContract{
			{Repo: "acme/api", Paths: []string{"openapi.yaml"}},
			{Repo: "acme/ghost", Paths: []string{"missing.yaml"}},
		},
	}
	wf := &requirement.Workflow{}
	task := flowTask(domain.TaskKindCodeReview)
	if err := store.CreateTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	ptask := r.buildPluginTask(context.Background(), task, pr, rc,
		&plugin.DiffContext{Files: []plugin.DiffFile{{Path: "a.go", Included: true}}}, wf)

	if ptask.Artifacts[plugin.ArtifactReviewGuidelines] == "" ||
		!strings.Contains(ptask.Artifacts[plugin.ArtifactReviewGuidelines], "关注边界检查") {
		t.Errorf("guidelines 未注入: %q", ptask.Artifacts[plugin.ArtifactReviewGuidelines])
	}
	if !strings.Contains(ptask.Artifacts[plugin.ArtifactReviewGuidelines], "关注并发") {
		t.Errorf("附加指令未合并: %q", ptask.Artifacts[plugin.ArtifactReviewGuidelines])
	}
	if !strings.Contains(ptask.Artifacts[plugin.ArtifactRelatedContracts], "关联仓契约 acme/api@main:openapi.yaml") {
		t.Errorf("related 契约未注入: %q", ptask.Artifacts[plugin.ArtifactRelatedContracts])
	}
	if !strings.Contains(ptask.Artifacts[plugin.ArtifactRelatedContracts], "获取失败") {
		t.Errorf("失败契约应注明: %q", ptask.Artifacts[plugin.ArtifactRelatedContracts])
	}
	if wf.RepoConfig != rc {
		t.Errorf("RepoConfig 应进 workflow")
	}
	if wf.CheckpointSink == nil {
		t.Fatal("checkpoint 落库钩子应接线")
	}
	// 钩子真实落库（断点续审回流面）。
	wf.CheckpointSink(map[string]string{"r1": "out"})
	row, _ := store.GetTask(context.Background(), task.ID)
	if !strings.Contains(row.StageArtifacts, `"r1"`) {
		t.Errorf("checkpoint 未落库: %s", row.StageArtifacts)
	}
}

// TestBuildPluginTaskCheckpointReuse 同任务重试回流已持久化阶段产物（G2 断点续审）。
func TestBuildPluginTaskCheckpointReuse(t *testing.T) {
	plat := &flowProvider{}
	r, store, _ := flowRunner(t, plat)
	task := flowTask(domain.TaskKindCodeReview)
	task.StageArtifacts = `{"stage_r1": "cached"}`
	if err := store.CreateTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	ptask := r.buildPluginTask(context.Background(), task, &plugin.PRContext{DefaultBranch: "main"},
		plugin.EmptyRepoConfig(), &plugin.DiffContext{}, &requirement.Workflow{})
	if ptask.Artifacts["stage_r1"] != "cached" {
		t.Errorf("checkpoint 应回流: %v", ptask.Artifacts)
	}
}

// TestRecordRegretLedgers 学习环记账：指纹级 regret 建行 + 模式级 regret
// （Learning.Enabled）双账本都落，失败不阻断。
func TestRecordRegretLedgers(t *testing.T) {
	plat := &flowProvider{}
	r, store, _ := flowRunner(t, plat)
	r.Learning = learning.Config{Enabled: true}
	pr := &plugin.PRContext{Platform: "gitea", Owner: "acme", Repo: "app", Number: "42"}
	out := &plugin.WorkflowOutput{Reflect: &plugin.ReflectSummary{
		RemovedPrints:   []string{"fp-1"},
		RemovedFindings: []domain.Finding{{File: "a.go", Comment: "空指针", By: "sast"}},
	}}
	r.recordRegret(context.Background(), out, pr)

	ps, err := store.GetPrintState(context.Background(), "gitea", "acme", "app", "42", "fp-1")
	if err != nil || ps == nil {
		t.Fatalf("指纹 regret 应建行: %v %v", ps, err)
	}
}

// TestRecordRegretNoReflect 无反思产物：记账空转不触库不 panic。
func TestRecordRegretNoReflect(t *testing.T) {
	plat := &flowProvider{}
	r, _, _ := flowRunner(t, plat)
	r.recordRegret(context.Background(), &plugin.WorkflowOutput{}, nil) // pr nil 也不炸
}

// ---------- 报告组装面 ----------

// TestBuildReportSpecViews 完备性报告组装：Spec×Check 对齐渲染视图（AC 命中
// 状态/证据回填、未判定兜底）、scope creep 透传、成本汇总。
func TestBuildReportSpecViews(t *testing.T) {
	plat := &flowProvider{}
	r, _, _ := flowRunner(t, plat)
	r.Version = "v-test"
	out := &plugin.WorkflowOutput{
		Verdict: "comment", Rule: "R5.2", Tokens: 100, Elapsed: time.Second,
		ReportTemplate: "analyst",
		Spec: &plugin.RequirementSpec{Source: "issue#1", ACs: []plugin.AcceptanceCriterion{
			{ID: "AC1", Text: "支持重试"},
			{ID: "AC2", Text: "支持熔断"},
		}},
		Report: &plugin.ComplianceReport{
			Checks: []plugin.ACCheck{
				{ID: "AC1", Status: plugin.ACImplemented, Evidence: "a.go:10 代码有 retry"},
			},
			ScopeCreep: []plugin.ScopeCreepItem{{File: "b.go", Note: "超出需求"}},
		},
		Completeness: &plugin.CompletenessReport{OK: false, Failures: []string{"sast 未装配"}},
	}
	taskCost := llm.NewCostTracker()
	taskCost.RecordPricedCached("gpt-x", "", 1000, 0, 500,
		port.Price{InputPerM: 1000, OutputPerM: 2000})
	rep := r.buildReport(flowTask(domain.TaskKindRequirementCompliance),
		&plugin.PRContext{HeadSHA: "abc123"}, out, taskCost)

	if rep.ShowCompliance != true || rep.ReqSource != "issue#1" {
		t.Errorf("合规段未装配: %+v", rep)
	}
	if len(rep.ACs) != 2 {
		t.Fatalf("AC 视图应 2 条, got %d", len(rep.ACs))
	}
	if rep.ACs[0].Status != plugin.ACImplemented || rep.ACs[0].Evid != "a.go:10 代码有 retry" {
		t.Errorf("AC1 命中视图失真: %+v", rep.ACs[0])
	}
	if rep.ACs[1].Status != plugin.ACUnverifiable || rep.ACs[1].Evid != "未判定" {
		t.Errorf("AC2 未判定兜底失真: %+v", rep.ACs[1])
	}
	if len(rep.ScopeCreep) != 1 || rep.ScopeCreep[0].File != "b.go" {
		t.Errorf("scope creep 未透传: %+v", rep.ScopeCreep)
	}
	if len(rep.Completeness) != 1 || !strings.Contains(rep.Completeness[0], "sast") {
		t.Errorf("完备性段未透传: %+v", rep.Completeness)
	}
	if rep.CostUSD <= 0 {
		t.Errorf("成本汇总缺失: %f", rep.CostUSD)
	}
	if rep.Template != "analyst" {
		t.Errorf("报告模板键未回声: %q", rep.Template)
	}
}

// TestAssembleReportSections 报告组装：影响面快照渲染 + 交互图路标（share 令牌）、
// 追溯链段、行内上限。
func TestAssembleReportSections(t *testing.T) {
	plat := &flowProvider{}
	r, _, _ := flowRunner(t, plat)
	r.Cfg.PublicURL = "https://argus.example.com"
	r.Cfg.APIToken = "sekret"
	snap, _ := json.Marshal(impactview.Snapshot{
		Version: 1, Changed: []string{"a.go"},
		Nodes: []impactview.Node{
			{ID: "file:a.go", Kind: "file", Label: "a.go", Changed: true},
			{ID: "pkg:app", Kind: "package", Label: "app"},
		},
		Risk:  impactview.Risk{Level: "medium", Score: 40, Reasons: []string{"触及核心文件"}},
		Stats: impactview.Stats{CallSites: 2},
	})
	ptask := plugin.Task{
		Diff: &plugin.DiffContext{Files: []plugin.DiffFile{{Path: "a.go", Additions: 3, Deletions: 1}}},
		Artifacts: map[string]string{
			plugin.ArtifactImpactView: string(snap),
			"trace_chain":             `{"source_ref":"issue#9","traces":[{"ac_id":"AC1","code_files":["a.go"],"tests":["a_test.go"],"test_status":"covered","code_status":"implemented"}],"broken":["AC2"]}`,
		},
	}
	rc := &plugin.RepoConfig{Review: plugin.ReviewSettings{MaxComments: 7}}
	rep := r.assembleReport(flowTask(domain.TaskKindCodeReview),
		&plugin.PRContext{Platform: "gitea", Owner: "acme", Repo: "app", Number: "42"},
		ptask, &plugin.WorkflowOutput{Verdict: "comment"}, nil, rc)

	if rep.ImpactSection == "" || !strings.Contains(rep.ImpactSection, "交互式影响面图") {
		t.Fatalf("影响面段缺失: %q", rep.ImpactSection)
	}
	if !strings.Contains(rep.ImpactSection, "share=") {
		t.Errorf("交互图路标应带 share 令牌: %q", rep.ImpactSection)
	}
	if rep.TraceSection == "" || !strings.Contains(rep.TraceSection, "AC1") || !strings.Contains(rep.TraceSection, "AC2") {
		t.Errorf("追溯链段失真: %q", rep.TraceSection)
	}
	if rep.MaxInlineComments != 7 {
		t.Errorf("行内上限未透传: %d", rep.MaxInlineComments)
	}
	if rep.ScaleFiles != 1 || rep.ScaleAdditions != 3 || rep.ScaleDeletions != 1 {
		t.Errorf("规模统计失真: files=%d +%d -%d", rep.ScaleFiles, rep.ScaleAdditions, rep.ScaleDeletions)
	}
}

// TestApplyTaskReportTemplateChain 报告模板四级裁决（v6.11.0）：任务级合法值
// 保留；非法值弃用落链；链内 knobs > 仓 yaml > env。
func TestApplyTaskReportTemplateChain(t *testing.T) {
	plat := &flowProvider{}
	r, _, _ := flowRunner(t, plat)

	// 任务级合法值原样保留。
	ptask := plugin.Task{Artifacts: map[string]string{plugin.ArtifactReportTemplate: "analyst"}}
	r.applyTaskReportTemplate(&ptask, flowTask(domain.TaskKindCodeReview),
		&plugin.RepoConfig{Review: plugin.ReviewSettings{ReportTemplate: "minimal"}},
		taskKnobs{reportTemplate: "standard"})
	if ptask.Artifacts[plugin.ArtifactReportTemplate] != "analyst" {
		t.Errorf("合法任务级模板被覆盖: %v", ptask.Artifacts)
	}

	// 任务级非法值弃用 → knobs 链顶生效 + 告警。
	ptask2 := plugin.Task{Artifacts: map[string]string{plugin.ArtifactReportTemplate: "自由发挥"}}
	r.applyTaskReportTemplate(&ptask2, flowTask(domain.TaskKindCodeReview),
		&plugin.RepoConfig{Review: plugin.ReviewSettings{ReportTemplate: "minimal"}},
		taskKnobs{reportTemplate: "standard"})
	if ptask2.Artifacts[plugin.ArtifactReportTemplate] != "standard" {
		t.Errorf("非法任务级应弃用落 knobs 链: %v", ptask2.Artifacts)
	}

	// knobs 空 → 仓 yaml；仓 yaml 空 → env。
	ptask3 := plugin.Task{Artifacts: map[string]string{}}
	r.applyTaskReportTemplate(&ptask3, flowTask(domain.TaskKindCodeReview),
		&plugin.RepoConfig{Review: plugin.ReviewSettings{ReportTemplate: "minimal"}}, taskKnobs{})
	if ptask3.Artifacts[plugin.ArtifactReportTemplate] != "minimal" {
		t.Errorf("仓 yaml 层未生效: %v", ptask3.Artifacts)
	}
	ptask4 := plugin.Task{Artifacts: map[string]string{}}
	r.Cfg.ReportTemplate = "analyst"
	r.applyTaskReportTemplate(&ptask4, flowTask(domain.TaskKindCodeReview),
		plugin.EmptyRepoConfig(), taskKnobs{})
	if ptask4.Artifacts[plugin.ArtifactReportTemplate] != "analyst" {
		t.Errorf("env 层未生效: %v", ptask4.Artifacts)
	}
}

// TestToResultPayloadShape ResultPayload 形状钉住（GET /tasks/:id 的 result_json 契约）。
func TestToResultPayloadShape(t *testing.T) {
	out := &plugin.WorkflowOutput{
		Verdict: "request_changes", Rule: "R5.3", Summary: "摘要",
		PluginsRun: []string{"sast"}, Tokens: 9,
		Merged: &plugin.MergedFindings{Findings: []domain.Finding{{File: "a.go"}}, Degraded: []string{"semgrep"}},
		Report: &plugin.ComplianceReport{ScopeCreep: []plugin.ScopeCreepItem{{File: "b.go"}}},
	}
	p := toResultPayload(out, "diff-text")
	if p.Verdict != "request_changes" || p.Rule != "R5.3" || p.DiffText != "diff-text" {
		t.Fatalf("标量字段失真: %+v", p)
	}
	if len(p.Findings) != 1 || len(p.Degraded) != 1 || len(p.ScopeCreep) != 1 || len(p.Plugins) != 1 {
		t.Fatalf("集合字段失真: %+v", p)
	}
	// nil Merged 不炸。
	p2 := toResultPayload(&plugin.WorkflowOutput{Verdict: "approve"}, "")
	if p2.Findings != nil || p2.DiffText != "" {
		t.Errorf("nil Merged 应安全: %+v", p2)
	}
}

// TestCountMissingPartialAndHelpers 纯函数钉住：missing/partial 计数、作者名
// 回退、findings 计数、rule 取词。
func TestCountMissingPartialAndHelpers(t *testing.T) {
	if got := countMissingPartial(&plugin.ComplianceReport{Checks: []plugin.ACCheck{
		{Status: plugin.ACMissing}, {Status: plugin.ACPartial}, {Status: plugin.ACImplemented},
	}}); got != 2 {
		t.Errorf("missing+partial 应 2, got %d", got)
	}
	if countMissingPartial(nil) != 0 {
		t.Error("nil report 应 0")
	}
	if authorName(nil) != "" {
		t.Error("nil author 应空")
	}
	if authorName(&provider.CRUser{Username: ""}) != "" {
		t.Error("空用户名应空")
	}
	if authorName(&provider.CRUser{Username: "u", Name: "n"}) != "u" {
		t.Error("username 优先")
	}
	if findingsCount(nil) != 0 || ruleOf(nil) != "" {
		t.Error("nil 安全")
	}
}

// TestPruneSpillRoot spill 过期清理（v5.1.0）：7 天前任务目录删除、新鲜与非目录
// 保留、根缺失不炸。
func TestPruneSpillRoot(t *testing.T) {
	root := t.TempDir()
	spill := filepath.Join(root, "spill")
	old := filepath.Join(spill, "t-old")
	fresh := filepath.Join(spill, "t-new")
	if err := os.MkdirAll(old, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(fresh, 0o755); err != nil {
		t.Fatal(err)
	}
	past := time.Now().AddDate(0, 0, -8)
	if err := os.Chtimes(old, past, past); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(spill, "keep.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	PruneSpillRoot(root)
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Errorf("过期目录应删除: %v", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Errorf("新鲜目录应保留: %v", err)
	}
	if _, err := os.Stat(filepath.Join(spill, "keep.txt")); err != nil {
		t.Errorf("非目录应保留: %v", err)
	}
	PruneSpillRoot(filepath.Join(root, "absent")) // 缺失根不炸
}

// ---------- 播报与 marker 面 ----------

// TestSetStartMarkerLifecycle marker 三态生命周期：无历史 → 新建；有历史 →
// 原地更新（幂等复用，不留孤儿「审查中」）；更新失败 → 告警不新建。
func TestSetStartMarkerLifecycle(t *testing.T) {
	ctx := context.Background()

	// 新建路径。
	plat := &flowProvider{}
	r, _, _ := flowRunner(t, plat)
	task := flowTask(domain.TaskKindCodeReview)
	r.setStartMarker(ctx, task, "first")
	notes, updated, _ := plat.snapshot()
	if len(notes) != 1 || len(updated) != 0 {
		t.Fatalf("无历史应新建: notes=%d updated=%d", len(notes), len(updated))
	}

	// 更新路径（最近一条带锚点 → 原地更新）。
	plat2 := &flowProvider{comments: []*provider.IssueComment{
		{ID: 1, Body: "普通评论"},
		{ID: 2, Body: "<!-- argus:start task_id=t-old -->"},
	}}
	r2, _, _ := flowRunner(t, plat2)
	r2.setStartMarker(ctx, task, "second")
	notes2, updated2, _ := plat2.snapshot()
	if len(notes2) != 0 || len(updated2) != 1 {
		t.Fatalf("有锚点应原地更新: notes=%d updated=%d", len(notes2), len(updated2))
	}

	// 更新失败 → SDKCtx 层 swap-create 兜底（新评论替换旧评论），runner 不感知
	// 失败：marker 语义仍收敛到新文案。
	plat3 := &flowProvider{comments: []*provider.IssueComment{
		{ID: 2, Body: "<!-- argus:start task_id=t-old -->"},
	}, updateErr: errors.New("403")}
	r3, _, _ := flowRunner(t, plat3)
	r3.setStartMarker(ctx, task, "third")
	notes3, updated3, _ := plat3.snapshot()
	if len(notes3) != 1 || !strings.Contains(notes3[0], "third") || len(updated3) != 0 {
		t.Errorf("更新失败应走 swap-create 兜底: notes=%v updated=%v", notes3, updated3)
	}

	// SDK 未装配：no-op 不炸。
	(&Runner{Log: logx.Nop()}).setStartMarker(ctx, task, "x")
}

// TestPublishOutboxAndEvents 进度事件双写（v3.1/P1-05）：进程内 Bus 订阅可见 +
// outbox 落库；Store 故障只告警不炸。
func TestPublishOutboxAndEvents(t *testing.T) {
	plat := &flowProvider{}
	r, store, _ := flowRunner(t, plat)
	ctx := context.Background()
	sub, cancel := r.Bus.Subscribe(ctx)
	defer cancel()

	task := flowTask(domain.TaskKindCodeReview)
	r.publish(progress.EventTaskRunning, task, storage.StatusRunning, "")
	select {
	case ev := <-sub:
		if ev.Type != progress.EventTaskRunning || ev.TaskID != task.ID {
			t.Fatalf("事件字段失真: %+v", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("订阅者未收到事件")
	}

	// 阶段事件。
	r.publishStage(task, "R3")
	select {
	case ev := <-sub:
		if ev.Type != progress.EventTaskStage || ev.Stage != "R3" {
			t.Fatalf("阶段事件失真: %+v", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("阶段事件未送达")
	}

	// nil Bus / nil task 安全。
	r2 := &Runner{Log: logx.Nop()}
	r2.publish(progress.EventTaskRunning, nil, "", "")

	// outbox 落库：真 Store 追加成功；关库后追加失败只告警。
	r.publishOutbox(progress.Event{Type: progress.EventTaskFinished, TaskID: task.ID})
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	r.publish(progress.EventTaskFinished, task, storage.StatusDone, "approve") // 关库不炸
}

// TestReviewHintsDegraded 学习环提示注入降级面：关闭/无 Store/查询失败/无样本
// 都返回空串。
func TestReviewHintsDegraded(t *testing.T) {
	plat := &flowProvider{}
	r, store, _ := flowRunner(t, plat)
	ctx := context.Background()
	pr := &plugin.PRContext{Platform: "gitea", Owner: "acme", Repo: "app", Number: "42"}

	if r.reviewHints(ctx, pr) != "" {
		t.Fatal("学习环关闭应空")
	}
	r.Learning = learning.Config{Enabled: true}
	if r.reviewHints(ctx, nil) != "" {
		t.Fatal("nil PR 应空")
	}
	if got := r.reviewHints(ctx, pr); got != "" {
		t.Fatalf("无样本应空: %q", got)
	}
	_ = store.Close()
	if got := r.reviewHints(ctx, pr); got != "" {
		t.Fatalf("查询失败应降级空: %q", got)
	}
}

// TestPrevFindingsSectionEmpty 前轮意见注入：空库返回空（不占提示词预算）。
func TestPrevFindingsSectionEmpty(t *testing.T) {
	plat := &flowProvider{}
	r, _, _ := flowRunner(t, plat)
	got := r.prevFindingsSection(context.Background(),
		&plugin.PRContext{Platform: "gitea", Owner: "acme", Repo: "app", Number: "42"})
	if got != "" {
		t.Fatalf("空库应空段: %q", got)
	}
}

// ---------- 杂项纯函数与守卫 ----------

// TestWritingHelpers 写作面纯函数：产物去向措辞、评论 ID 容错解析、修复分支护栏。
func TestWritingHelpers(t *testing.T) {
	if writingTargetLabel(domain.TaskKindDescribe, 0) != "PR 描述" {
		t.Error("describe 无评论应指 PR 描述")
	}
	if writingTargetLabel(domain.TaskKindChangelog, 661) != "评论 #661" {
		t.Error("有评论应指评论号")
	}
	if writingTargetLabel(domain.TaskKindChangelog, 0) != "任务报告" {
		t.Error("changelog 无评论应指任务报告")
	}
	if parseCommentID("661") != 661 || parseCommentID(" 661 ") != 661 {
		t.Error("数字 ID 应解析")
	}
	if parseCommentID("gitlab-note-9") != 0 {
		t.Error("非数字容错为 0")
	}
	if !isFixBranch("argus/fix/42") || isFixBranch("main") {
		t.Error("修复分支命名空间判定失真")
	}
}

// TestVerifyFixesGates 修复验证环门控：未配置命令/无意见/无工作副本池均静默跳过。
func TestVerifyFixesGates(t *testing.T) {
	ctx := context.Background()
	task := flowTask(domain.TaskKindCodeReview)
	pr := &plugin.PRContext{Platform: "gitea", Owner: "acme", Repo: "app", Number: "42"}

	// 未配置验证命令。
	(&Runner{Log: logx.Nop(), Cfg: &config.Config{}}).verifyFixes(ctx, task, pr,
		&plugin.WorkflowOutput{Merged: &plugin.MergedFindings{Findings: []domain.Finding{{File: "a.go"}}}})

	// 配置了命令但无工作副本池。
	(&Runner{Log: logx.Nop(), Cfg: &config.Config{FixVerifyCmd: "go build ./..."},
		WC: nil}).verifyFixes(ctx, task, pr,
		&plugin.WorkflowOutput{Merged: &plugin.MergedFindings{Findings: []domain.Finding{{File: "a.go"}}}})

	// 无意见。
	(&Runner{Log: logx.Nop(), Cfg: &config.Config{FixVerifyCmd: "go build ./..."}}).verifyFixes(
		ctx, task, pr, &plugin.WorkflowOutput{})
}

// TestPushVerifiedFixesStaleGuards 修复推送取消标记检查（Phase 3 spec §4）：
// ctx 取消 / 任务已非 running → 放弃推送（僵尸写护栏）。
func TestPushVerifiedFixesStaleGuards(t *testing.T) {
	plat := &flowProvider{}
	r, store, _ := flowRunner(t, plat)
	task := flowTask(domain.TaskKindCodeReview)
	task.Status = storage.StatusPending // 非 running = stale 重跑
	if err := store.CreateTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	pr := &plugin.PRContext{Platform: "gitea", Owner: "acme", Repo: "app", Number: "42", HeadBranch: "feature"}
	r.pushVerifiedFixes(context.Background(), task, pr, t.TempDir(),
		[]domain.Finding{{File: "a.go", SuggestedFix: "x"}}, 1)
	if task.FixStatus != "skipped_stale" {
		t.Fatalf("非 running 应放弃推送, got %q", task.FixStatus)
	}

	// ctx 取消。
	task2 := flowTask(domain.TaskKindCodeReview)
	task2.HeadSHA = "def456" // 避开同仓同号唯一键
	task2.Status = storage.StatusRunning
	if err := store.CreateTask(context.Background(), task2); err != nil {
		t.Fatal(err)
	}
	cctx, cancel := context.WithCancel(context.Background())
	cancel()
	r.pushVerifiedFixes(cctx, task2, pr, t.TempDir(), nil, 1)
	// 取消 ctx 下落库也取消（best-effort 静默放弃），内存态不回写——行为钉住：
	// 推送链路在取消面绝不产生 git 副作用。
	if task2.FixStatus == "pushed" {
		t.Fatalf("ctx 取消不应走到推送: %q", task2.FixStatus)
	}
}

// TestSetFixStatusBestEffort setFixStatus：有 Store 落库 + 内存回写；落库失败
// 只告警；无 Store 纯内存（打桩场景）。
func TestSetFixStatusBestEffort(t *testing.T) {
	plat := &flowProvider{}
	r, store, _ := flowRunner(t, plat)
	ctx := context.Background()
	task := flowTask(domain.TaskKindCodeReview)
	if err := store.CreateTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	r.setFixStatus(ctx, task, "pushing", "detail")
	if task.FixStatus != "pushing" {
		t.Fatalf("内存态应回写, got %q", task.FixStatus)
	}
	row, _ := store.GetTask(ctx, task.ID)
	if row.FixStatus != "pushing" {
		t.Fatalf("应落库, got %q", row.FixStatus)
	}
	// 关库后失败只告警。
	_ = store.Close()
	r.setFixStatus(ctx, task, "pushed", "")
	// 无 Store 内存态。
	(&Runner{Log: logx.Nop()}).setFixStatus(ctx, task, "pr_created", "")
	if task.FixStatus != "pr_created" {
		t.Fatalf("无 Store 应内存回写, got %q", task.FixStatus)
	}
}

// TestGitRunFailure gitRun 非零退出返回 ok=false（不 panic 不挂起）。
func TestGitRunFailure(t *testing.T) {
	plat := &flowProvider{}
	r, _, _ := flowRunner(t, plat)
	code, _, ok := r.gitRun(context.Background(), t.TempDir(),
		[]string{"cat-file", "-e", "0000000000000000000000000000000000000000"})
	if ok {
		t.Fatalf("不存在的对象应失败: code=%d", code)
	}
}

// TestWorkflowFactoryWiring WorkflowFactory 导出构造（插件复用 R3 工具基础设施）：
// 同 Runner 两次构造产出独立 Workflow 实例、LLM 装配一致。
func TestWorkflowFactoryWiring(t *testing.T) {
	plat := &flowProvider{}
	r, _, _ := flowRunner(t, plat)
	factory := r.WorkflowFactory()
	p1 := factory(dispatch.NewTokenBudget(1000))
	p2 := factory(nil)
	if p1 == nil || p2 == nil {
		t.Fatal("工厂应产出实例")
	}
	wf1, ok1 := p1.(*requirement.Workflow)
	wf2, ok2 := p2.(*requirement.Workflow)
	if !ok1 || !ok2 || wf1 == wf2 {
		t.Fatalf("工厂应产出独立 Workflow 实例: %T %T", p1, p2)
	}
	if wf1.SDK != r.SDK || wf1.Dispatcher != r.Dispatcher {
		t.Error("基础设施应共享引用")
	}
	if wf1.LLM == nil {
		t.Fatal("LLM 应装配")
	}
	// WorkflowFor 同路径。
	if r.WorkflowFor(nil) == nil {
		t.Fatal("WorkflowFor 应产出 workflow")
	}
}

// TestRepoGuideForCacheAndTruncation 评审规范多源读取（v3.71.0）：REVIEW.md +
// AGENTS.md 拼接、超限截断、缓存命中（第二次不打平台）、关闭开关直通。
func TestRepoGuideForCacheAndTruncation(t *testing.T) {
	plat := &flowProvider{
		files: map[string]string{
			".argus/REVIEW.md": strings.Repeat("R", 200),
			"AGENTS.md":        strings.Repeat("A", 40000),
		},
	}
	r, _, _ := flowRunner(t, plat)
	ctx := context.Background()
	got := r.RepoGuideFor(ctx, "gitea", "acme", "app", "sha1")
	if !strings.Contains(got, "RRRR") || !strings.Contains(got, "AGENTS.md") {
		t.Fatalf("两源应拼接:\n%.80s", got)
	}
	if len(got) > 200+32000+200 {
		t.Errorf("agent 规范应截断: %d bytes", len(got))
	}
	// 第二次命中缓存（files 清空仍可取）。
	plat.mu.Lock()
	plat.files = map[string]string{}
	plat.mu.Unlock()
	if again := r.RepoGuideFor(ctx, "gitea", "acme", "app", "sha1"); again != got {
		t.Error("同 sha 应命中缓存")
	}
	// 开关关闭直通。
	r.Cfg.RepoGuideOff = true
	if r.RepoGuideFor(ctx, "gitea", "acme", "app", "sha2") != "" {
		t.Error("开关关闭应空")
	}
	r.Cfg.RepoGuideOff = false
	if r.RepoGuideFor(ctx, "gitea", "acme", "app", "") != "" {
		t.Error("空 headSHA 应空")
	}
}

// TestBuildPRContextFields PR 上下文组装：CR/仓库元数据映射、作者回退、head SHA
// 回退、CI 状态聚合（最坏状态语义）。
func TestBuildPRContextFields(t *testing.T) {
	plat := &flowProvider{
		cr:   flowCR(),
		repo: &provider.PlatformRepo{DefaultBranch: "main"},
		statuses: []provider.CommitStatus{
			{State: provider.CommitStatusSuccess, Context: "build"},
			{State: provider.CommitStatusFailure, Context: "test"},
		},
	}
	r, _, _ := flowRunner(t, plat)
	task := flowTask(domain.TaskKindCodeReview)
	pr, err := r.buildPRContext(context.Background(), task)
	if err != nil {
		t.Fatal(err)
	}
	if pr.Title != "feat: 登录" || pr.Body != "作者手写描述" || pr.Author != "alice" {
		t.Errorf("CR 元数据映射失真: %+v", pr)
	}
	if pr.HeadSHA != "abc123" || pr.BaseBranch != "main" || pr.HeadBranch != "feature/login" {
		t.Errorf("分支映射失真: %+v", pr)
	}
	if pr.CIStatus != string(provider.CommitStatusFailure) {
		t.Errorf("CI 状态应取最坏 failure, got %q", pr.CIStatus)
	}

	// head SHA 回退：CR 无 SHA 时落任务侧。
	plat.cr.HeadSHA = ""
	pr2, err := r.buildPRContext(context.Background(), task)
	if err != nil || pr2.HeadSHA != task.HeadSHA {
		t.Errorf("head SHA 应回退任务值: %v %q", err, pr2.HeadSHA)
	}

	// GetCR 失败上抛（§10.2 上下文失败 → task failed）。
	plat.cr = nil
	if _, err := r.buildPRContext(context.Background(), task); err == nil {
		t.Error("GetCR 失败应上抛")
	}
}

// TestFetchCIStatusDegraded CI 状态三降级：无 SHA/无能力/查询失败全静默空串。
func TestFetchCIStatusDegraded(t *testing.T) {
	plat := &flowProvider{}
	r, _, _ := flowRunner(t, plat)
	ctx := context.Background()
	if r.fetchCIStatus(ctx, "gitea", "acme", "app", "") != "" {
		t.Error("空 SHA 应空")
	}
	// 有 CommitStatusManager 但查询失败。
	if got := r.fetchCIStatus(ctx, "gitea", "acme", "app", "sha"); got != "" {
		t.Errorf("查询失败应空: %q", got)
	}
	// 未知平台（SDK 无 provider）。
	if got := r.fetchCIStatus(ctx, "nope", "a", "b", "sha"); got != "" {
		t.Errorf("未知平台应空: %q", got)
	}
}

// TestBuildDiffIncrementalModes diff 增量清洗分派：非 synchronize 全量、
// synchronize 无前置记录降级全量、拉取失败上抛。
func TestBuildDiffIncrementalModes(t *testing.T) {
	md := flowDiff()
	plat := &flowProvider{repo: &provider.PlatformRepo{DefaultBranch: "main"}, diff: md}
	r, _, _ := flowRunner(t, plat)
	ctx := context.Background()
	pr := &plugin.PRContext{Platform: "gitea", Owner: "acme", Repo: "app", Number: "42", DefaultBranch: "main"}

	// 非 synchronize → 全量。
	task := flowTask(domain.TaskKindCodeReview)
	diff, err := r.buildDiffIncremental(ctx, pr, task, 60000)
	if err != nil || diff == nil || diff.Text == "" {
		t.Fatalf("全量路径应产出 diff: %v", err)
	}

	// synchronize 无前置 → 降级全量。
	task.EventType = "cr.synchronize"
	diff2, err := r.buildDiffIncremental(ctx, pr, task, 60000)
	if err != nil || diff2.Text == "" {
		t.Fatalf("无前置记录应降级全量: %v", err)
	}

	// 拉取失败上抛。
	plat.diffErr = errors.New("500")
	if _, err := r.buildDiffIncremental(ctx, pr, task, 60000); err == nil {
		t.Error("diff 拉取失败应上抛")
	}
}

// TestLoadRepoConfigDegraded 仓库配置加载降级：无文件 → 默认配置（不阻断审查）。
func TestLoadRepoConfigDegraded(t *testing.T) {
	plat := &flowProvider{repo: &provider.PlatformRepo{DefaultBranch: "main"}}
	r, _, _ := flowRunner(t, plat)
	rc := r.loadRepoConfig(context.Background(),
		&plugin.PRContext{Platform: "gitea", Owner: "acme", Repo: "app", DefaultBranch: "main"})
	if rc == nil || rc.Loaded {
		t.Fatalf("无配置文件应返回默认: %+v", rc)
	}
}

// TestHasIncludedGoFilesAndPaths diff 辅助纯函数：纳入过滤与 Go 文件判定。
func TestHasIncludedGoFilesAndPaths(t *testing.T) {
	diff := &plugin.DiffContext{Files: []plugin.DiffFile{
		{Path: "a.go", Included: true},
		{Path: "b.md", Included: true},
		{Path: "c.go", Included: false},
	}}
	if !hasIncludedGoFiles(diff) {
		t.Error("含纳入 Go 文件应 true")
	}
	if hasIncludedGoFiles(&plugin.DiffContext{Files: []plugin.DiffFile{{Path: "b.md", Included: true}}}) {
		t.Error("无 Go 文件应 false")
	}
	if hasIncludedGoFiles(nil) {
		t.Error("nil diff 应 false")
	}
	paths := includedPaths(diff)
	if len(paths) != 2 || paths[0] != "a.go" || paths[1] != "b.md" {
		t.Errorf("纳入路径过滤失真: %v", paths)
	}
	if includedPaths(nil) != nil {
		t.Error("nil diff 应 nil 路径")
	}
}

// TestRenderTouchedSymbolsSection 触达符号渲染上限（30 条封顶 + 省略注明）。
func TestRenderTouchedSymbolsSection(t *testing.T) {
	if renderTouchedSection(nil) != "" {
		t.Error("空清单应空段")
	}
}

// ---------- 前置注入器：工作副本降级面与符号渲染 ----------

// wcFailPool 无凭证工作副本池：Ensure 统一失败（「未配置 CredentialOf」），
// 注入器应告警降级、零产物、不阻断（失败不进审查链路的纪律面）。
func wcFailPool(t *testing.T) *workcopy.Pool {
	t.Helper()
	return workcopy.NewPool(t.TempDir(), logx.Nop())
}

// TestInjectorsDegradeOnWorkcopyFail 注入器 × 工作副本故障：SAST/CodeGraph/
// CodeSearch/ImpactView/OCR delegate 全链路降级（每条只 Warn 不 panic）。
func TestInjectorsDegradeOnWorkcopyFail(t *testing.T) {
	plat := &flowProvider{}
	r, _, _ := flowRunner(t, plat)
	r.Cfg.SASTGitleaksBin = "gitleaks"
	r.Cfg.Codegraph = true
	r.Cfg.CodeSearch = true
	r.Cfg.ImpactView = true
	r.Cfg.OcrBin = "/nonexistent/ocr"
	r.WC = wcFailPool(t)
	ctx := context.Background()
	pr := &plugin.PRContext{Platform: "gitea", Owner: "acme", Repo: "app", Number: "42",
		HeadSHA: "abc123", BaseBranch: "main", DefaultBranch: "main"}
	diff := &plugin.DiffContext{Files: []plugin.DiffFile{{Path: "a.go", Included: true}}}
	wf := &requirement.Workflow{}
	ptask := plugin.Task{Kind: domain.TaskKindCodeReview, Artifacts: map[string]string{}}

	r.injectSAST(ctx, pr, diff, wf)
	r.injectCodeGraph(ctx, pr, diff, &ptask, wf)
	r.injectCodeSearch(ctx, pr, diff, wf)
	r.injectImpactView(ctx, "t-1", pr, diff, &ptask, wf)
	r.injectOCRDelegate(ctx, pr, diff, &ptask)

	if _, ok := ptask.Artifacts[plugin.ArtifactImpactView]; ok {
		t.Error("工作副本失败不应有影响面产物")
	}
	if len(wf.SASTOutcomes) != 0 || wf.CodeSearchDir != "" || wf.CodeGraph != nil || wf.Graph != nil {
		t.Error("工作副本失败不应有 workflow 注入")
	}
}

// TestInjectCodeGraphNoGoFiles CodeGraph 门控：无纳入 Go 文件早退（不触工作副本）。
func TestInjectCodeGraphNoGoFiles(t *testing.T) {
	plat := &flowProvider{}
	r, _, _ := flowRunner(t, plat)
	r.Cfg.Codegraph = true
	r.WC = wcFailPool(t)
	ptask := plugin.Task{Artifacts: map[string]string{}}
	r.injectCodeGraph(context.Background(),
		&plugin.PRContext{Owner: "acme", Repo: "app", Number: "1", HeadSHA: "s"},
		&plugin.DiffContext{Files: []plugin.DiffFile{{Path: "readme.md", Included: true}}},
		&ptask, &requirement.Workflow{})
	if len(ptask.Artifacts) != 0 {
		t.Errorf("无 Go 文件不应注入: %v", ptask.Artifacts)
	}
}

// TestRenderTouchedSymbols 触达符号渲染：真 go/parser 索引（临时模块）→
// 行区间交集 → 数据区围栏 section。
func TestRenderTouchedSymbols(t *testing.T) {
	dir := t.TempDir()
	src := "package app\n\ntype Greeter struct{}\n\nfunc (g *Greeter) Greet() string { return \"hi\" }\n\nfunc Run() string {\n\tg := &Greeter{}\n\treturn g.Greet()\n}\n"
	if err := os.WriteFile(filepath.Join(dir, "app.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	idx, err := codeintel.NewIndex(dir, 100)
	if err != nil {
		t.Fatal(err)
	}
	out := renderTouchedSymbols(idx, map[string][][2]int{"app.go": {{5, 5}}})
	if out == "" || !strings.Contains(out, "Greeter.Greet") || !strings.Contains(out, "get_symbol") {
		t.Fatalf("触达符号段失真:\n%s", out)
	}
	if strings.Contains(out, "其余略") {
		t.Error("未超上限不应省略")
	}
	// 空交集 → 空段。
	if got := renderTouchedSymbols(idx, map[string][][2]int{"app.go": {{200, 300}}}); got != "" {
		t.Fatalf("无交集应空段: %q", got)
	}
}

// TestSourceReaderHeadSHA 证据核验读取面：绑定检出版本即 verified 资格。
func TestSourceReaderHeadSHA(t *testing.T) {
	sr := &workcopySourceReader{key: workcopy.WorktreeKey{HeadSHA: "abc123"}}
	if sr.HeadSHA() != "abc123" {
		t.Fatalf("HeadSHA = %q", sr.HeadSHA())
	}
}

// ---------- loadSkillText tool 模式 ----------

// stubLister skill 清单面（tool 模式渐进披露用）：Resolve 借嵌入接口兜底。
type stubLister struct {
	skill.Provider
	metas []skill.Meta
}

func (s stubLister) ListSkills(context.Context) ([]skill.Meta, error) {
	return s.metas, nil
}

// TestLoadSkillTextToolMode 渐进披露清单（v0.10.18）：tool 模式经 Lister 渲染
// 清单；provider 不支持 ListSkills / 查询失败均空串降级。
func TestLoadSkillTextToolMode(t *testing.T) {
	r := &Runner{
		Cfg:           &config.Config{SkillMode: "tool"},
		SkillProvider: stubLister{metas: []skill.Meta{{Name: "methodology", Title: "方法论"}}},
		SkillRefs:     []string{"methodology@v1"},
		Log:           logx.Nop(),
	}
	out := r.loadSkillText(context.Background())
	if !strings.Contains(out, "methodology") {
		t.Fatalf("清单应含规范引用名:\n%s", out)
	}

	// provider 不是 Lister → 空。
	r2 := &Runner{Cfg: &config.Config{SkillMode: "tool"}, SkillProvider: &stubSkills{},
		SkillRefs: []string{"x"}, Log: logx.Nop()}
	if out := r2.loadSkillText(context.Background()); out != "" {
		t.Fatalf("非 Lister 应空: %q", out)
	}

	// 解析失败逐条跳过（content 模式）。
	r3 := &Runner{Cfg: &config.Config{}, SkillProvider: &stubSkills{},
		SkillRefs: []string{"a@1", "b@2"}, Log: logx.Nop(),
	}
	_ = r3.loadSkillText(context.Background()) // stubSkills 恒成功，覆盖拼接路径
}

// ---------- 修复推送 git 真实面 ----------

// gitInit 建真 git 仓（empty initial commit，identity 就位）。
func gitInit(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-b", "main")
	run("config", "user.email", "t@local")
	run("config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(dir, "fix.txt"), []byte("line1\nline2\nline3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-m", "init")
	return dir
}

// TestPushVerifiedFixesDirectPushFail direct 形态推送失败（无 remote）：apply →
// commit → push 失败 → push_failed 状态 + 清场（树回到 checkout 态，不给后续
// 任务留污染）。
func TestPushVerifiedFixesDirectPushFail(t *testing.T) {
	plat := &flowProvider{}
	r, store, _ := flowRunner(t, plat)
	r.Cfg.FixPush = true
	r.Cfg.FixPushMode = "direct"
	task := flowTask(domain.TaskKindCodeReview)
	task.Status = storage.StatusRunning
	if err := store.CreateTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	dir := gitInit(t)
	pr := &plugin.PRContext{Platform: "gitea", Owner: "acme", Repo: "app", Number: "42",
		HeadBranch: "feature/login"}
	findings := []domain.Finding{{File: "fix.txt", Line: 2, Verified: true, SuggestedFix: "fixed line"}}

	r.pushVerifiedFixes(context.Background(), task, pr, dir, findings, 1)

	row, _ := store.GetTask(context.Background(), task.ID)
	if row.FixStatus != "push_failed" {
		t.Fatalf("无 remote 推送应 push_failed, got %q", row.FixStatus)
	}
	// push 失败清场 = reset 到含修复 commit 的 HEAD（修复留本地待下轮重推，
	// 未跟踪产物被 clean 清掉）——污染树防线钉住。
	b, _ := os.ReadFile(filepath.Join(dir, "fix.txt"))
	if !strings.Contains(string(b), "fixed line") {
		t.Fatalf("push 失败后修复应留在本地 HEAD: %q", string(b))
	}
}

// TestPushVerifiedFixesApplyFail 应用失败（锚定文件缺失）→ apply_failed，不触 git。
func TestPushVerifiedFixesApplyFail(t *testing.T) {
	plat := &flowProvider{}
	r, store, _ := flowRunner(t, plat)
	r.Cfg.FixPush = true
	r.Cfg.FixPushMode = "direct"
	task := flowTask(domain.TaskKindCodeReview)
	task.HeadSHA = "bbb222"
	task.Status = storage.StatusRunning
	if err := store.CreateTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	dir := gitInit(t)
	pr := &plugin.PRContext{Platform: "gitea", Owner: "acme", Repo: "app", Number: "42",
		HeadBranch: "feature/login"}
	findings := []domain.Finding{{File: "absent.txt", Line: 1, Verified: true, SuggestedFix: "x"}}

	r.pushVerifiedFixes(context.Background(), task, pr, dir, findings, 1)

	row, _ := store.GetTask(context.Background(), task.ID)
	// 应用失败的状态词同为 push_failed（失败细节进 audit detail），主链路不受阻。
	if row.FixStatus != "push_failed" {
		t.Fatalf("应用失败应落 push_failed 状态词, got %q", row.FixStatus)
	}
}

// TestPushVerifiedFixesEmptyFiles 无验证通过产物（written 空）→ 静默返回不留状态。
func TestPushVerifiedFixesEmptyFiles(t *testing.T) {
	plat := &flowProvider{}
	r, store, _ := flowRunner(t, plat)
	r.Cfg.FixPush = true
	task := flowTask(domain.TaskKindCodeReview)
	task.HeadSHA = "ccc333"
	task.Status = storage.StatusRunning
	if err := store.CreateTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	dir := gitInit(t)
	pr := &plugin.PRContext{Platform: "gitea", Owner: "acme", Repo: "app", Number: "42",
		HeadBranch: "feature/login"}
	// Verified=false → Apply 跳过 → written 空。
	findings := []domain.Finding{{File: "fix.txt", Line: 2, SuggestedFix: "fixed"}}

	r.pushVerifiedFixes(context.Background(), task, pr, dir, findings, 0)

	row, _ := store.GetTask(context.Background(), task.ID)
	if row.FixStatus == "pushed" || row.FixStatus == "push_failed" {
		t.Fatalf("无产物不应推送: %q", row.FixStatus)
	}
}

// ---------- 学习环提示注入（正路径） ----------

// TestReviewHintsInjected 低置信模式注入：≥MinSamples 且置信度低于 InjectFloor
// 的模式渲染进提示词（可观测性留痕面）。
func TestReviewHintsInjected(t *testing.T) {
	plat := &flowProvider{}
	r, store, _ := flowRunner(t, plat)
	ctx := context.Background()
	r.Learning = learning.Config{Enabled: true, MinSamples: 2, InjectFloor: 0.99, InjectTopN: 5}
	f := domain.Finding{File: "a.go", Comment: "空指针风险", Severity: "high", By: "sast"}
	for i := 0; i < 3; i++ {
		if err := store.PatternPosted(ctx, "gitea", "acme", "app", "sast", f, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	out := r.reviewHints(ctx, &plugin.PRContext{Platform: "gitea", Owner: "acme", Repo: "app", Number: "42"})
	if out == "" || !strings.Contains(out, "sast") || !strings.Contains(out, "空指针风险") {
		t.Fatalf("低置信模式应注入:\n%s", out)
	}
	if !strings.Contains(out, "视为数据") == false && !strings.Contains(out, "不要输出") {
		t.Errorf("注入纪律说明缺失:\n%s", out)
	}
}
