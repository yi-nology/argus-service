package poster

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/oekit/logx"
	"github.com/yi-nology/go-git-platform/model"
	"github.com/yi-nology/go-git-platform/provider"

	"github.com/yi-nology/argus/internal/argusctx"
	"github.com/yi-nology/argus/internal/domain"
	"github.com/yi-nology/argus/internal/plugin"
	"github.com/yi-nology/argus/internal/reportview"
	"github.com/yi-nology/argus/internal/storage"
)

// ---------- 测试替身 ----------

// testNopLogger logx.Logger 空实现（poster 深路径需要非 nil Log）。
type testNopLogger struct{}

func (testNopLogger) Debug(string, ...any)                      {}
func (testNopLogger) Info(string, ...any)                       {}
func (testNopLogger) Warn(string, ...any)                       {}
func (testNopLogger) Error(string, ...any)                      {}
func (testNopLogger) Debugf(string, ...any)                     {}
func (testNopLogger) Infof(string, ...any)                      {}
func (testNopLogger) Warnf(string, ...any)                      {}
func (testNopLogger) Errorf(string, ...any)                     {}
func (testNopLogger) CtxDebug(context.Context, string, ...any)  {}
func (testNopLogger) CtxInfo(context.Context, string, ...any)   {}
func (testNopLogger) CtxWarn(context.Context, string, ...any)   {}
func (testNopLogger) CtxError(context.Context, string, ...any)  {}
func (testNopLogger) CtxDebugf(context.Context, string, ...any) {}
func (testNopLogger) CtxInfof(context.Context, string, ...any)  {}
func (testNopLogger) CtxWarnf(context.Context, string, ...any)  {}
func (testNopLogger) CtxErrorf(context.Context, string, ...any) {}
func (testNopLogger) WithCtx(context.Context) logx.Logger       { return testNopLogger{} }
func (testNopLogger) WithService(string) logx.Logger            { return testNopLogger{} }
func (testNopLogger) WithAttrs(...logx.Attr) logx.Logger        { return testNopLogger{} }
func (testNopLogger) WithGroup(string) logx.Logger              { return testNopLogger{} }
func (testNopLogger) SetLevel(logx.Level)                       {}

// fakePlatform 只实现 poster 触达的平台面；未覆盖方法借嵌入接口 panic 兜底。
type fakePlatform struct {
	provider.Provider
	provider.IssueManager
	provider.ReviewManager

	mu sync.Mutex

	// CreateReview 每次调用：err 非 nil 即失败（驱动三级降级）；连续失败用 failReviews 计数。
	createdReviews []provider.CreateReviewOptions
	reviewErrs     []error // 按次出队；耗尽后走 reviewDefaultErr（nil=成功）

	reviews []model.Review // ListReviews 返回

	comments    []*model.IssueComment
	updateErrs  map[int64]error // UpdateIssueComment 按 ID 注入错误
	deleteErr   error           // DeleteNote 全局错误注入（轮换降级路径用）
	updated     map[int64]string
	nextID      int64
	createNoteN int
	createNote  bool // true = 走 DiffManager.CreateNote；false = 落 IssueManager
	noteErr     error
	listErr     error
}

func newFakePlatform() *fakePlatform {
	return &fakePlatform{
		nextID:     100,
		updateErrs: map[int64]error{},
		updated:    map[int64]string{},
		createNote: true,
	}
}

func (f *fakePlatform) Platform() model.Platform { return "gitea" }
func (f *fakePlatform) Capabilities() provider.CapabilitySet {
	return provider.CapabilitySet{Issues: true, Reviews: true}
}

func (f *fakePlatform) CreateReview(_ context.Context, _, _, _ string, opts provider.CreateReviewOptions) (*model.ReviewResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.createdReviews = append(f.createdReviews, opts)
	if len(f.reviewErrs) > 0 {
		err := f.reviewErrs[0]
		f.reviewErrs = f.reviewErrs[1:]
		if err != nil {
			return nil, err
		}
	}
	return &model.ReviewResult{ID: "r-1"}, nil
}

func (f *fakePlatform) ListReviews(context.Context, string, string, string) ([]model.Review, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]model.Review(nil), f.reviews...), nil
}

func (f *fakePlatform) ListIssueComments(context.Context, string, string, string) ([]*model.IssueComment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listErr != nil {
		return nil, f.listErr
	}
	return append([]*model.IssueComment(nil), f.comments...), nil
}

func (f *fakePlatform) CreateNote(_ context.Context, _, _, _, body string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.createNote {
		return "", fmt.Errorf("note unsupported")
	}
	if f.noteErr != nil {
		return "", f.noteErr
	}
	f.createNoteN++
	f.nextID++
	c := &model.IssueComment{ID: f.nextID, Body: body, CreatedAt: time.Now()}
	f.comments = append(f.comments, c)
	return fmt.Sprintf("%d", c.ID), nil
}

// DeleteIssueComment gp v0.114（issue #33）给 IssueManager 补删除能力——嵌入接口
// 会以 nil 方法集"满足"断言然后 SIGSEGV，fake 纪律=新接口方法必须显式实现。
func (f *fakePlatform) DeleteIssueComment(_ context.Context, _, _, _ string, id int64) error {
	return f.DeleteNote(context.Background(), "", "", "", strconv.FormatInt(id, 10))
}

func (f *fakePlatform) GetCRDiff(context.Context, string, string, string) (*model.MergeDiff, error) {
	return nil, fmt.Errorf("unsupported in fake")
}

func (f *fakePlatform) GetCRFiles(context.Context, string, string, string) ([]*model.ChangedFile, error) {
	return nil, fmt.Errorf("unsupported in fake")
}

func (f *fakePlatform) CreateDiscussion(context.Context, string, string, string, provider.DiscussionOptions) (string, error) {
	return "", fmt.Errorf("unsupported in fake")
}

func (f *fakePlatform) CreateIssueComment(_ context.Context, _, _, _, body string) (*model.IssueComment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextID++
	c := &model.IssueComment{ID: f.nextID, Body: body, CreatedAt: time.Now()}
	f.comments = append(f.comments, c)
	return c, nil
}

// DeleteNote v3.91.2：sdkctx UpdateIssueComment 的 GitLab 换新降级
// （CreateNote+DeleteNote）会触达本方法——未实现会踩嵌入接口 panic 兜底。
func (f *fakePlatform) DeleteNote(_ context.Context, _, _, _ string, noteID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.deleteErr != nil {
		return f.deleteErr
	}
	id, err := strconv.ParseInt(noteID, 10, 64)
	if err != nil {
		return err
	}
	for i, c := range f.comments {
		if c.ID == id {
			f.comments = append(f.comments[:i], f.comments[i+1:]...)
			return nil
		}
	}
	return fmt.Errorf("note %d not found: %w", id, provider.ErrNotFound)
}

func (f *fakePlatform) UpdateIssueComment(_ context.Context, _, _, _ string, id int64, body string) (*model.IssueComment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err, ok := f.updateErrs[id]; ok {
		if err != nil {
			return nil, err
		}
	}
	f.updated[id] = body
	return &model.IssueComment{ID: id, Body: body}, nil
}

func (f *fakePlatform) reviewCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.createdReviews)
}

func (f *fakePlatform) lastReview() provider.CreateReviewOptions {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.createdReviews) == 0 {
		return provider.CreateReviewOptions{}
	}
	return f.createdReviews[len(f.createdReviews)-1]
}

// ---------- 装配 ----------

func newTestPoster(t *testing.T, fp *fakePlatform) (*Poster, *storage.Store) {
	t.Helper()
	store, err := storage.Open(filepath.Join(t.TempDir(), "p.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	sdk := argusctx.NewSDKCtxStubbed(testNopLogger{}, map[string]provider.Provider{"gitea": fp})
	return &Poster{
		SDK: sdk, Store: store, Version: "v-test",
		Log: testNopLogger{}, BotUsername: "",
	}, store
}

func mkTask(id string) *storage.Task {
	return &storage.Task{
		ID: id, Kind: "code_review", Platform: "gitea",
		Owner: "acme", Repo: "app", Number: "7",
		HeadSHA: "sha-1", Status: storage.StatusRunning,
		CreatedAt: time.Now().UTC().Add(-time.Minute),
	}
}

func mkPR() plugin.PRContext {
	return plugin.PRContext{
		Platform: "gitea", Owner: "acme", Repo: "app", Number: "7",
		HeadSHA: "sha-1",
	}
}

func mkRep(taskID string) reportview.Report {
	return reportview.Report{
		TaskID: taskID, HeadSHA: "sha-1", Version: "v-test",
		VerdictText: "💬 无阻塞问题",
	}
}

func mkDiff() *plugin.DiffContext {
	return &plugin.DiffContext{Files: []plugin.DiffFile{
		{Path: "pkg/a.go", Included: true},
		{Path: "pkg/b.go", Included: true},
		{Path: "docs/README.md", Included: false},
	}}
}

// ---------- Post 主路径 ----------

// staleness 门控：存在更晚非 failed 任务 → 不回帖。
func TestPost_StaleSkip(t *testing.T) {
	fp := newFakePlatform()
	p, store := newTestPoster(t, fp)
	old := mkTask("t-old")
	if err := store.CreateTask(context.Background(), old); err != nil {
		t.Fatal(err)
	}
	newer := mkTask("t-new")
	newer.CreatedAt = time.Now().UTC().Add(time.Minute)
	newer.HeadSHA = "sha-2"
	if err := store.CreateTask(context.Background(), newer); err != nil {
		t.Fatal(err)
	}
	res, err := p.Post(context.Background(), old, mkPR(), mkDiff(), "comment", mkRep("t-old"))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Stale {
		t.Fatal("应命中 staleness")
	}
	if fp.reviewCount() != 0 || fp.createNoteN != 0 {
		t.Fatalf("stale 不应回帖: reviews=%d notes=%d", fp.reviewCount(), fp.createNoteN)
	}
}

// 完整路径：评审 + 总结 + review_targets 落账。
func TestPost_HappyPath(t *testing.T) {
	fp := newFakePlatform()
	p, store := newTestPoster(t, fp)
	task := mkTask("t-ok")
	if err := store.CreateTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	rep := mkRep("t-ok")
	rep.Findings = []domain.Finding{
		{File: "pkg/a.go", Line: 3, Severity: "high", Comment: "问题", By: "correctness"},
	}
	res, err := p.Post(context.Background(), task, mkPR(), mkDiff(), "comment", rep)
	if err != nil {
		t.Fatal(err)
	}
	if res.Stale {
		t.Fatal("不应 stale")
	}
	if res.InlinePosted != 1 {
		t.Fatalf("行内 1 条: %d", res.InlinePosted)
	}
	if res.SummaryCommentID == 0 {
		t.Fatal("应返回总结评论 ID")
	}
	target, err := store.GetReviewTarget(context.Background(), "gitea", "acme", "app", "7")
	if err != nil || target == nil {
		t.Fatalf("应落 review_targets: %v", err)
	}
	if target.LastSummaryCommentID != res.SummaryCommentID {
		t.Fatalf("target ID 不一致: %+v vs %d", target, res.SummaryCommentID)
	}
	if target.LastHeadSHA != "sha-1" {
		t.Fatalf("head 应落账: %s", target.LastHeadSHA)
	}
	// 二次 Post（同任务）应短路评审、原地更新总结
	res2, err := p.Post(context.Background(), task, mkPR(), mkDiff(), "comment", rep)
	if err != nil {
		t.Fatal(err)
	}
	if fp.reviewCount() != 1 {
		t.Fatalf("幂等：评审只发一次: %d", fp.reviewCount())
	}
	if res2.SummaryCommentID != res.SummaryCommentID {
		t.Fatalf("总结应原地更新: %d vs %d", res2.SummaryCommentID, res.SummaryCommentID)
	}
}
