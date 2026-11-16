package argusctx

// 平台调用包装面测试（GetCR/CreateCR/diff/评论/评审/协作者）：用能力打桩验证
// 路由优先级、降级链、选项透传与 PII 出站改写——全部本地假实现，无网络。

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/yi-nology/go-git-platform/provider"
)

// capsFake 实现 Provider（含 FileManager/DiffManager 等内嵌能力）+ 可选能力接口。
// 未覆写的方法借嵌入接口 panic 兜底——测试不应触达。
type capsFake struct {
	provider.Provider
	provider.IssueManager
	provider.ReviewManager
	provider.NoteManager
	provider.ReactionManager
	provider.CollaboratorManager

	cr        *provider.ChangeRequest
	crs       []*provider.ChangeRequest
	total     *int
	mergeDiff *provider.MergeDiff
	repo      *provider.PlatformRepo
	fileBody  string
	fileErr   error
	issue     *provider.Issue
	comments  []*provider.IssueComment
	collabs   []*provider.Collaborator

	noteID string

	listNotesErr, issueCommentsErr     error
	createNoteErr, updateNoteErr       error
	createCommentErr, updateCommentErr error
	deleteNoteErr, deleteIssueErr      error
	createReviewErr, listReviewsErr    error
	reactionErr                        error
	webhookEvent                       *provider.NormalizedEvent
	webhookErr                         error

	getCRN, createCRN, getDiffN, listCRN, repoN, fileN, issueN int
	listNotesN, listCommentsN, createNoteN, createCommentN     int
	updateNoteN, updateCommentN, deleteNoteN, deleteCommentN   int
	reactionN, collabN, createReviewN, listReviewsN            int
	lastFileRef                                                string
	lastNoteBody                                               string
	lastDeletedNoteID                                          string
	lastReviewOpts                                             provider.CreateReviewOptions
	lastCommentBody                                            string
}

func (f *capsFake) GetCR(context.Context, string, string, string) (*provider.ChangeRequest, error) {
	f.getCRN++
	return f.cr, nil
}

func (f *capsFake) CreateCR(_ context.Context, opts provider.CreateCROptions) (*provider.ChangeRequest, error) {
	f.createCRN++
	return f.cr, nil
}

func (f *capsFake) GetCRDiff(context.Context, string, string, string) (*provider.MergeDiff, error) {
	f.getDiffN++
	return f.mergeDiff, nil
}

func (f *capsFake) ListCRs(context.Context, provider.ListCROptions) ([]*provider.ChangeRequest, *int, error) {
	f.listCRN++
	return f.crs, f.total, nil
}

func (f *capsFake) GetRepo(context.Context, string, string) (*provider.PlatformRepo, error) {
	f.repoN++
	return f.repo, nil
}

func (f *capsFake) GetFileContent(_ context.Context, _, _, _, ref string) (string, error) {
	f.fileN++
	f.lastFileRef = ref
	return f.fileBody, f.fileErr
}

func (f *capsFake) GetIssue(context.Context, string, string, string) (*provider.Issue, error) {
	f.issueN++
	return f.issue, nil
}

func (f *capsFake) ListNotes(context.Context, string, string, string) ([]*provider.IssueComment, error) {
	f.listNotesN++
	return f.comments, f.listNotesErr
}

func (f *capsFake) ListIssueComments(context.Context, string, string, string) ([]*provider.IssueComment, error) {
	f.listCommentsN++
	return f.comments, f.issueCommentsErr
}

func (f *capsFake) CreateNote(_ context.Context, _, _, _, body string) (string, error) {
	f.createNoteN++
	f.lastNoteBody = body
	return f.noteID, f.createNoteErr
}

func (f *capsFake) DeleteNote(_ context.Context, _, _, _, noteID string) error {
	f.deleteNoteN++
	f.lastDeletedNoteID = noteID
	return f.deleteNoteErr
}

func (f *capsFake) CreateIssueComment(_ context.Context, _, _, _, body string) (*provider.IssueComment, error) {
	f.createCommentN++
	f.lastCommentBody = body
	return &provider.IssueComment{ID: 99, Body: body}, f.createCommentErr
}

func (f *capsFake) UpdateNote(_ context.Context, _, _, _, noteID, body string) (*provider.IssueComment, error) {
	f.updateNoteN++
	f.lastCommentBody = body
	return &provider.IssueComment{ID: 1, Body: body}, f.updateNoteErr
}

func (f *capsFake) UpdateIssueComment(_ context.Context, _, _, _ string, commentID int64, body string) (*provider.IssueComment, error) {
	f.updateCommentN++
	f.lastCommentBody = body
	return &provider.IssueComment{ID: commentID, Body: body}, f.updateCommentErr
}

func (f *capsFake) DeleteIssueComment(_ context.Context, _, _, _ string, _ int64) error {
	f.deleteCommentN++
	return f.deleteIssueErr
}

func (f *capsFake) AddIssueCommentReaction(context.Context, string, string, int64, string) (*provider.Reaction, error) {
	f.reactionN++
	return &provider.Reaction{}, f.reactionErr
}

func (f *capsFake) ListCollaborators(context.Context, string, string) ([]*provider.Collaborator, error) {
	f.collabN++
	return f.collabs, nil
}

func (f *capsFake) CreateReview(_ context.Context, _, _, _ string, opts provider.CreateReviewOptions) (*provider.ReviewResult, error) {
	f.createReviewN++
	f.lastReviewOpts = opts
	return &provider.ReviewResult{}, f.createReviewErr
}

func (f *capsFake) ListReviews(context.Context, string, string, string) ([]provider.Review, error) {
	f.listReviewsN++
	return []provider.Review{{ID: 1}}, f.listReviewsErr
}

func (f *capsFake) ParseWebhookEvent(_ *http.Request, _ string) (*provider.NormalizedEvent, error) {
	return f.webhookEvent, f.webhookErr
}

func capsStub(f *capsFake) *SDKCtx {
	return NewSDKCtxStubbed(nopLogx{}, map[string]provider.Provider{"github": f, "gitlab": f})
}

func TestSDKCtxCRAndRepoWrappers(t *testing.T) {
	f := &capsFake{
		cr:        &provider.ChangeRequest{Number: "7", Title: "t"},
		crs:       []*provider.ChangeRequest{{Number: "8"}},
		mergeDiff: &provider.MergeDiff{RawDiff: "diff"},
		repo:      &provider.PlatformRepo{DefaultBranch: "main"},
		fileBody:  "content",
		issue:     &provider.Issue{Number: "3"},
		comments:  []*provider.IssueComment{{ID: 1}},
		collabs:   []*provider.Collaborator{{Username: "alice"}},
	}
	s := capsStub(f)
	ctx := context.Background()

	if got, err := s.GetCR(ctx, "github", "o", "r", "7"); err != nil || got.Number != "7" {
		t.Fatalf("GetCR = %+v, %v", got, err)
	}
	if _, err := s.CreateCR(ctx, "github", provider.CreateCROptions{Title: "t"}); err != nil || f.createCRN != 1 {
		t.Fatalf("CreateCR 未透传: err=%v n=%d", err, f.createCRN)
	}
	if got, err := s.GetCRDiff(ctx, "github", "o", "r", "7"); err != nil || got.RawDiff != "diff" {
		t.Fatalf("GetCRDiff = %+v, %v", got, err)
	}
	items, total, err := s.ListCRs(ctx, "github", provider.ListCROptions{})
	if err != nil || len(items) != 1 || total != 0 {
		t.Fatalf("ListCRs nil total 应归 0: items=%d total=%d err=%v", len(items), total, err)
	}
	ten := 10
	f.total = &ten
	if _, total, _ := s.ListCRs(ctx, "github", provider.ListCROptions{}); total != 10 {
		t.Fatalf("ListCRs 权威 total 未透传: %d", total)
	}
	if got, err := s.GetRepo(ctx, "github", "o", "r"); err != nil || got.DefaultBranch != "main" {
		t.Fatalf("GetRepo = %+v, %v", got, err)
	}
	if got, err := s.GetFileContent(ctx, "github", "o", "r", "a.go", "main"); err != nil || got != "content" {
		t.Fatalf("GetFileContent = %q, %v", got, err)
	}
	if f.lastFileRef != "main" {
		t.Fatalf("ref 未透传: %q", f.lastFileRef)
	}
	if got, err := s.GetIssue(ctx, "github", "o", "r", "3"); err != nil || got.Number != "3" {
		t.Fatalf("GetIssue = %+v, %v", got, err)
	}
	if got, err := s.ListCollaborators(ctx, "github", "o", "r"); err != nil || len(got) != 1 || got[0].Username != "alice" {
		t.Fatalf("ListCollaborators = %+v, %v", got, err)
	}
	if err := s.AddIssueCommentReaction(ctx, "github", "o", "r", 5, "eyes"); err != nil || f.reactionN != 1 {
		t.Fatalf("AddIssueCommentReaction err=%v n=%d", err, f.reactionN)
	}
	if got, err := s.ListReviews(ctx, "github", "o", "r", "7"); err != nil || len(got) != 1 || got[0].ID != 1 {
		t.Fatalf("ListReviews = %+v, %v", got, err)
	}
}

func TestListIssueCommentsPrefersNotesThenFallsBack(t *testing.T) {
	f := &capsFake{comments: []*provider.IssueComment{{ID: 11}}}
	s := capsStub(f)
	got, err := s.ListIssueComments(context.Background(), "gitlab", "o", "r", "1")
	if err != nil || len(got) != 1 || got[0].ID != 11 {
		t.Fatalf("NoteManager 主路失败: %+v %v", got, err)
	}
	if f.listNotesN != 1 || f.listCommentsN != 0 {
		t.Fatalf("主路命中不应回退: notes=%d issues=%d", f.listNotesN, f.listCommentsN)
	}

	f2 := &capsFake{comments: []*provider.IssueComment{{ID: 22}}, listNotesErr: errors.New("notes 404")}
	s2 := capsStub(f2)
	got, err = s2.ListIssueComments(context.Background(), "gitlab", "o", "r", "1")
	if err != nil || len(got) != 1 || got[0].ID != 22 {
		t.Fatalf("NoteManager 失败应回退 IssueManager: %+v %v", got, err)
	}
	if f2.listNotesN != 1 || f2.listCommentsN != 1 {
		t.Fatalf("回退计数错: notes=%d issues=%d", f2.listNotesN, f2.listCommentsN)
	}
}

func TestCreateIssueCommentRoutesAndMasksPII(t *testing.T) {
	withPIIPolicy(t, true)
	f := &capsFake{noteID: "123"}
	s := capsStub(f)
	got, err := s.CreateIssueComment(context.Background(), "gitlab", "o", "r", "1", "联系 13812345678")
	if err != nil || got.ID != 123 {
		t.Fatalf("CreateNote 主路失败: %+v %v", got, err)
	}
	if strings.Contains(f.lastNoteBody, "13812345678") || !strings.Contains(f.lastNoteBody, piiMaskNote) {
		t.Fatalf("出站正文未脱敏: %q", f.lastNoteBody)
	}
	if f.createCommentN != 0 {
		t.Fatal("主路成功不应回退 IssueManager")
	}

	// note ID 不可解析：留 ID=0（调用方按 0 视为定位失效），但仍返回评论。
	f2 := &capsFake{noteID: "not-a-number"}
	got, err = capsStub(f2).CreateIssueComment(context.Background(), "gitlab", "o", "r", "1", "body")
	if err != nil || got.ID != 0 {
		t.Fatalf("不可解析 note id 应 ID=0: %+v %v", got, err)
	}

	// CreateNote 失败 → IssueManager 回退。
	f3 := &capsFake{createNoteErr: errors.New("notes api 500")}
	got, err = capsStub(f3).CreateIssueComment(context.Background(), "github", "o", "r", "1", "body")
	if err != nil || got.ID != 99 || f3.createCommentN != 1 {
		t.Fatalf("CreateNote 失败应回退 IssueManager: %+v %v (n=%d)", got, err, f3.createCommentN)
	}
}

func TestUpdateIssueCommentRouting(t *testing.T) {
	ctx := context.Background()

	// 1) NoteManager 命中。
	f := &capsFake{}
	if got, err := capsStub(f).UpdateIssueComment(ctx, "gitlab", "o", "r", "1", 5, "b"); err != nil || got.ID != 1 {
		t.Fatalf("UpdateNote 主路: %+v %v", got, err)
	}
	if f.updateNoteN != 1 || f.updateCommentN != 0 {
		t.Fatalf("主路命中不应回退: note=%d issue=%d", f.updateNoteN, f.updateCommentN)
	}

	// 2) NoteManager 失败 → IssueManager 命中。
	f2 := &capsFake{updateNoteErr: errors.New("no notes")}
	if _, err := capsStub(f2).UpdateIssueComment(ctx, "github", "o", "r", "1", 5, "b"); err != nil {
		t.Fatalf("IssueManager 回退失败: %v", err)
	}
	if f2.updateNoteN != 1 || f2.updateCommentN != 1 {
		t.Fatalf("回退计数错: note=%d issue=%d", f2.updateNoteN, f2.updateCommentN)
	}

	// 3) 两路皆败 → DiffManager 原子换新（CreateNote + DeleteNote 旧 ID）。
	f3 := &capsFake{
		updateNoteErr: errors.New("no notes"), updateCommentErr: errors.New("no edit"),
		noteID: "77",
	}
	got, err := capsStub(f3).UpdateIssueComment(ctx, "gitlab", "o", "r", "1", 5, "b")
	if err != nil || got.ID != 77 {
		t.Fatalf("换新兜底失败: %+v %v", got, err)
	}
	if f3.createNoteN != 1 || f3.deleteNoteN != 1 {
		t.Fatalf("换新应 CreateNote+DeleteNote: create=%d delete=%d", f3.createNoteN, f3.deleteNoteN)
	}
	if f3.lastDeletedNoteID != strconv.Itoa(5) {
		t.Fatalf("删除的旧 note ID = %q, want 5", f3.lastDeletedNoteID)
	}

	// 4) 换新时新 note ID 不可解析 → ID=0（缓存定位失效但评论已建）。
	f4 := &capsFake{
		updateNoteErr: errors.New("no notes"), updateCommentErr: errors.New("no edit"),
		noteID: "abc",
	}
	got, err = capsStub(f4).UpdateIssueComment(ctx, "gitlab", "o", "r", "1", 5, "b")
	if err != nil || got.ID != 0 {
		t.Fatalf("不可解析新 note id 应 ID=0: %+v %v", got, err)
	}

	// 5) 换新 CreateNote 也失败 → 返回原 IssueManager 错误。
	f5 := &capsFake{
		updateNoteErr: errors.New("no notes"), updateCommentErr: errors.New("no edit"),
		createNoteErr: errors.New("create failed"),
	}
	_, err = capsStub(f5).UpdateIssueComment(ctx, "gitlab", "o", "r", "1", 5, "b")
	if err == nil || !strings.Contains(err.Error(), "no edit") {
		t.Fatalf("换新失败应返回原编辑错误, got %v", err)
	}

	// 6) 换新 DeleteNote 失败只告警，仍返回新评论。
	f6 := &capsFake{
		updateNoteErr: errors.New("no notes"), updateCommentErr: errors.New("no edit"),
		noteID: "88", deleteNoteErr: errors.New("delete failed"),
	}
	got, err = capsStub(f6).UpdateIssueComment(ctx, "gitlab", "o", "r", "1", 5, "b")
	if err != nil || got.ID != 88 {
		t.Fatalf("DeleteNote 失败不应阻断换新: %+v %v", got, err)
	}
}

func TestDeleteIssueCommentRouting(t *testing.T) {
	ctx := context.Background()
	f := &capsFake{}
	if err := capsStub(f).DeleteIssueComment(ctx, "github", "o", "r", "1", 5); err != nil {
		t.Fatalf("IssueManager 主路: %v", err)
	}
	if f.deleteCommentN != 1 || f.deleteNoteN != 0 {
		t.Fatalf("主路命中不应回退: issue=%d note=%d", f.deleteCommentN, f.deleteNoteN)
	}

	f2 := &capsFake{deleteIssueErr: errors.New("no delete")}
	if err := capsStub(f2).DeleteIssueComment(ctx, "gitlab", "o", "r", "1", 6); err != nil {
		t.Fatalf("DiffManager 兜底: %v", err)
	}
	if f2.deleteNoteN != 1 || f2.lastDeletedNoteID != strconv.Itoa(6) {
		t.Fatalf("兜底删除参数错: n=%d id=%q", f2.deleteNoteN, f2.lastDeletedNoteID)
	}

	f3 := &capsFake{deleteIssueErr: errors.New("no delete"), deleteNoteErr: errors.New("no note")}
	if err := capsStub(f3).DeleteIssueComment(ctx, "gitlab", "o", "r", "1", 7); err == nil {
		t.Fatal("两路皆败应返回错误")
	}
}

func TestCreateReviewMasksPIIAndDelegates(t *testing.T) {
	withPIIPolicy(t, true)
	f := &capsFake{}
	opts := provider.CreateReviewOptions{
		Body:     "总结 手机 13812345678",
		Comments: []provider.ReviewComment{{Path: "a.go", Body: "证件 " + validResidentID()}},
	}
	if _, err := capsStub(f).CreateReview(context.Background(), "github", "o", "r", "1", opts); err != nil {
		t.Fatalf("CreateReview: %v", err)
	}
	if f.createReviewN != 1 {
		t.Fatal("未调用 ReviewManager")
	}
	if strings.Contains(f.lastReviewOpts.Body, "13812345678") || !strings.Contains(f.lastReviewOpts.Body, piiMaskNote) {
		t.Fatalf("总评未脱敏: %q", f.lastReviewOpts.Body)
	}
	if strings.Contains(f.lastReviewOpts.Comments[0].Body, validResidentID()) {
		t.Fatalf("行内评论未脱敏: %q", f.lastReviewOpts.Comments[0].Body)
	}
}

// bareFake 只实现 Provider（不含可选能力接口）：验证 unsupportedErr 路径。
type bareFake struct{ provider.Provider }

func TestUnsupportedCapabilities(t *testing.T) {
	s := NewSDKCtxStubbed(nopLogx{}, map[string]provider.Provider{"gitea": bareFake{}})
	ctx := context.Background()

	if _, err := s.GetIssue(ctx, "gitea", "o", "r", "1"); err == nil ||
		!strings.Contains(err.Error(), "不支持 IssueManager") {
		t.Fatalf("GetIssue 应 unsupported, got %v", err)
	}
	if _, err := s.ListCollaborators(ctx, "gitea", "o", "r"); err == nil ||
		!strings.Contains(err.Error(), "不支持 CollaboratorManager") {
		t.Fatalf("ListCollaborators 应 unsupported, got %v", err)
	}
	if err := s.AddIssueCommentReaction(ctx, "gitea", "o", "r", 1, "eyes"); err == nil ||
		!strings.Contains(err.Error(), "不支持 ReactionManager") {
		t.Fatalf("Reaction 应 unsupported, got %v", err)
	}
	if _, err := s.CreateReview(ctx, "gitea", "o", "r", "1", provider.CreateReviewOptions{}); err == nil ||
		!strings.Contains(err.Error(), "不支持 ReviewManager") {
		t.Fatalf("CreateReview 应 unsupported, got %v", err)
	}
	if _, err := s.ListReviews(ctx, "gitea", "o", "r", "1"); err == nil ||
		!strings.Contains(err.Error(), "不支持 ReviewManager") {
		t.Fatalf("ListReviews 应 unsupported, got %v", err)
	}
	if _, err := s.GetCR(ctx, "unknown", "o", "r", "1"); err == nil {
		t.Fatal("未配置平台应报错")
	}
	if _, _, err := s.ListCRs(ctx, "unknown", provider.ListCROptions{}); err == nil {
		t.Fatal("未配置平台 ListCRs 应报错")
	}
	// ListIssueComments：NoteManager 与 IssueManager 均缺失 → unsupported。
	if _, err := s.ListIssueComments(ctx, "gitea", "o", "r", "1"); err == nil ||
		!strings.Contains(err.Error(), "不支持 IssueManager") {
		t.Fatalf("无评论能力应 unsupported, got %v", err)
	}
	// DeleteIssueComment 的能力组合由 capsFake 覆盖（bareFake 的 DiffManager
	// 来自内嵌 nil 接口，调用会 panic——不作为兜底路径的测试对象）。
}
