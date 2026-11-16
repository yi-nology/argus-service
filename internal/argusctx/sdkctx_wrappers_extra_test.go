package argusctx

// 平台调用包装面测试（GetCR/CreateCR/diff/评论/评审/协作者）：用能力打桩验证
// 路由优先级、降级链、选项透传与 PII 出站改写——全部本地假实现，无网络。

import (
	"context"
	"net/http"
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
