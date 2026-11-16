// reconcile_test.go —— FindTaskComment：报告头 marker 命中/未命中/错误。
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

func TestFindTaskCommentMatchesReportMarkerOnly(t *testing.T) {
	fp := newFakePlatform()
	fp.comments = []*model.IssueComment{
		nil, // nil 元素跳过
		{ID: 1, Body: "🔍 Argus 正在审查中\n\n<!-- argus:start task_id=t-1 -->"},
		{ID: 2, Body: "<!-- argus:report task_id=t-10 head=x ver=v1 -->\n另一个任务"},
		{ID: 3, Body: "<!-- argus:report task_id=t-1 head=abc ver=v1 -->\n本任务报告"},
		{ID: 4, Body: "<!-- argus:report task_id=t-1 head=def ver=v2 -->\n同任务后到报告"},
	}
	p := newPosterWithPlatform(fp)

	id, found, err := p.FindTaskComment(context.Background(), "gitea", "o", "r", "1", "t-1")
	if err != nil || !found || id != 3 {
		t.Fatalf("FindTaskComment = (%d,%v,%v), want (3,true,nil)", id, found, err)
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

	id, found, err := p.FindTaskComment(context.Background(), "gitea", "o", "r", "1", "t-1")
	if err != nil || found || id != 0 {
		t.Fatalf("FindTaskComment = (%d,%v,%v), want (0,false,nil)", id, found, err)
	}
}

func TestFindTaskCommentListError(t *testing.T) {
	boom := errors.New("platform down")
	fp := newFakePlatform()
	fp.listErr = boom
	p := newPosterWithPlatform(fp)

	if _, _, err := p.FindTaskComment(context.Background(), "gitea", "o", "r", "1", "t-1"); !errors.Is(err, boom) {
		t.Fatalf("列表错误应透传: %v", err)
	}
}

func TestFindTaskCommentRejectsInvalidWiring(t *testing.T) {
	ctx := context.Background()
	if _, _, err := (&Poster{}).FindTaskComment(ctx, "gitea", "o", "r", "1", "t-1"); err == nil {
		t.Fatal("SDK 未装配必须报错")
	}
	p := newPosterWithPlatform(newFakePlatform())
	if _, _, err := p.FindTaskComment(ctx, "gitea", "o", "r", "1", ""); err == nil {
		t.Fatal("空 task_id 必须报错")
	}
	if _, _, err := (*Poster)(nil).FindTaskComment(ctx, "gitea", "o", "r", "1", "t-1"); err == nil {
		t.Fatal("nil Poster 必须报错")
	}
}
