// reconcile_test.go —— FindTaskComment：严格报告头 + task/head/作者三重校验。
package poster

import (
	"context"
	"errors"
	"testing"

	"github.com/yi-nology/go-git-platform/model"
	"github.com/yi-nology/go-git-platform/provider"

	"github.com/yi-nology/argus/internal/argusctx"
)

func newPosterWithPlatform(fp *fakePlatform) *Poster {
	sdk := argusctx.NewSDKCtxStubbed(testNopLogger{}, map[string]provider.Provider{"gitea": fp})
	return &Poster{SDK: sdk, Log: testNopLogger{}}
}

func TestFindTaskCommentMatchesExactTaskAndHead(t *testing.T) {
	fp := newFakePlatform()
	fp.comments = []*model.IssueComment{
		nil, // nil 元素跳过
		{ID: 1, Body: "🔍 Argus 正在审查中\n\n<!-- argus:start task_id=t-1 -->"},
		{ID: 2, Body: "<!-- argus:report task_id=t-10 head=abc ver=v1 -->\n另一个任务"},
		{ID: 3, Body: "<!-- argus:report task_id=t-1 head=def ver=v1 -->\n同任务上一轮"},
		{ID: 4, Body: "正文引用 <!-- argus:report task_id=t-1 head=abc ver=v1 --> 不是独占行"},
		{ID: 5, Body: "<!-- argus:report task_id=t-1 head=abc ver=v1 -->\n本轮报告"},
		{ID: 6, Body: "<!-- argus:report task_id=t-1 head=abc ver=v2 -->\n本轮重复报告"},
	}
	p := newPosterWithPlatform(fp)

	id, found, err := p.FindTaskComment(context.Background(), "gitea", "o", "r", "1", "t-1", "abc")
	if err != nil || !found || id != 6 {
		t.Fatalf("FindTaskComment = (%d,%v,%v), want (6,true,nil)", id, found, err)
	}
}

func TestFindTaskCommentHeadMismatchIsAbsent(t *testing.T) {
	fp := newFakePlatform()
	fp.comments = []*model.IssueComment{
		{ID: 3, Body: "<!-- argus:report task_id=t-1 head=def ver=v1 -->"},
	}
	p := newPosterWithPlatform(fp)

	id, found, err := p.FindTaskComment(context.Background(), "gitea", "o", "r", "1", "t-1", "abc")
	if err != nil || found || id != 0 {
		t.Fatalf("head 不同不得互证: (%d,%v,%v)", id, found, err)
	}
}

func TestFindTaskCommentEmptyHeadMatchesEmptyOnly(t *testing.T) {
	fp := newFakePlatform()
	fp.comments = []*model.IssueComment{
		{ID: 1, Body: "<!-- argus:report task_id=t-1 head=abc ver=v1 -->"},
		{ID: 2, Body: "<!-- argus:report task_id=t-1 head= ver=v1 -->"},
	}
	p := newPosterWithPlatform(fp)

	id, found, err := p.FindTaskComment(context.Background(), "gitea", "o", "r", "1", "t-1", "")
	if err != nil || !found || id != 2 {
		t.Fatalf("空 head 只匹配 head=: (%d,%v,%v)", id, found, err)
	}
}

func TestFindTaskCommentAuthorSpoofRejected(t *testing.T) {
	fp := newFakePlatform()
	fp.comments = []*model.IssueComment{
		{ID: 1, Body: "<!-- argus:report task_id=t-1 head=abc ver=v1 -->", Author: &model.CRUser{Username: "attacker"}},
		{ID: 2, Body: "<!-- argus:report task_id=t-1 head=abc ver=v1 -->"}, // 缺作者也不认
		{ID: 3, Body: "<!-- argus:report task_id=t-1 head=abc ver=v1 -->", Author: &model.CRUser{Username: "argus-bot"}},
	}
	p := newPosterWithPlatform(fp)
	p.BotUsername = "argus-bot"

	id, found, err := p.FindTaskComment(context.Background(), "gitea", "o", "r", "1", "t-1", "abc")
	if err != nil || !found || id != 3 {
		t.Fatalf("作者伪造必须拒绝: (%d,%v,%v)", id, found, err)
	}
}

func TestFindTaskCommentAbsent(t *testing.T) {
	fp := newFakePlatform()
	fp.comments = []*model.IssueComment{
		{ID: 1, Body: "<!-- argus:start task_id=t-1 -->"},
		{ID: 2, Body: "普通评论"},
		{ID: 3, Body: "<!-- argus:report task_id=t-10 head=x ver=v1 -->"},
	}
	p := newPosterWithPlatform(fp)

	id, found, err := p.FindTaskComment(context.Background(), "gitea", "o", "r", "1", "t-1", "x")
	if err != nil || found || id != 0 {
		t.Fatalf("FindTaskComment = (%d,%v,%v), want (0,false,nil)", id, found, err)
	}
}

func TestFindTaskCommentListError(t *testing.T) {
	boom := errors.New("platform down")
	fp := newFakePlatform()
	fp.listErr = boom
	p := newPosterWithPlatform(fp)

	if _, _, err := p.FindTaskComment(context.Background(), "gitea", "o", "r", "1", "t-1", "x"); !errors.Is(err, boom) {
		t.Fatalf("列表错误应透传: %v", err)
	}
}

func TestFindTaskCommentRejectsInvalidWiring(t *testing.T) {
	ctx := context.Background()
	if _, _, err := (&Poster{}).FindTaskComment(ctx, "gitea", "o", "r", "1", "t-1", "x"); err == nil {
		t.Fatal("SDK 未装配必须报错")
	}
	p := newPosterWithPlatform(newFakePlatform())
	if _, _, err := p.FindTaskComment(ctx, "gitea", "o", "r", "1", "", "x"); err == nil {
		t.Fatal("空 task_id 必须报错")
	}
	if _, _, err := (*Poster)(nil).FindTaskComment(ctx, "gitea", "o", "r", "1", "t-1", "x"); err == nil {
		t.Fatal("nil Poster 必须报错")
	}
}
