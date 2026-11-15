// poster_summary_test.go —— 总结评论定位与轮换测试（自 poster_flow_test.go 拆出）。

package poster

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/yi-nology/go-git-platform/provider"
)

// ---------- postSummary 四级降级 ----------

func TestPostSummary_UpdateByCachedID(t *testing.T) {
	fp := newFakePlatform()
	p, store := newTestPoster(t, fp)
	if err := store.UpsertReviewTarget(context.Background(), "gitea", "acme", "app", "7", 42, "sha-1"); err != nil {
		t.Fatal(err)
	}
	id, err := p.postSummary(context.Background(), mkPR(), "## 报告")
	if err != nil || id != 42 {
		t.Fatalf("应回缓存 ID: id=%d err=%v", id, err)
	}
	if fp.updated[42] != "## 报告" {
		t.Fatalf("应更新评论 42: %v", fp.updated)
	}
	if fp.createNoteN != 0 {
		t.Fatalf("不应新建: %d", fp.createNoteN)
	}
}

// 404（评论被删）清缓存后走新建。
func TestPostSummary_CachedGoneClearsAndCreates(t *testing.T) {
	fp := newFakePlatform()
	fp.updateErrs[42] = fmt.Errorf("404: %w", provider.ErrNotFound)
	p, store := newTestPoster(t, fp)
	if err := store.UpsertReviewTarget(context.Background(), "gitea", "acme", "app", "7", 42, "sha-1"); err != nil {
		t.Fatal(err)
	}
	id, err := p.postSummary(context.Background(), mkPR(), "## 新报告")
	if err != nil {
		t.Fatal(err)
	}
	if id == 42 {
		t.Fatalf("缓存已失效，不应再返回 42")
	}
	target, _ := store.GetReviewTarget(context.Background(), "gitea", "acme", "app", "7")
	if target != nil && target.LastSummaryCommentID == 42 {
		t.Fatalf("404 后应清除缓存: %+v", target)
	}
	// v3.91.2 GitLab 换新降级：Update 404 → CreateNote+DeleteNote——旧评论应被
	// 删除（消灭重复报告），定位回写到新 ID。
	for _, c := range fp.comments {
		if c.ID == 42 {
			t.Fatalf("旧评论 42 应被换新删除，仍存在: %+v", fp.comments)
		}
	}
	if target == nil || target.LastSummaryCommentID != id {
		t.Fatalf("定位应回写新 ID %d: %+v", id, target)
	}
}

// 扫描兜底：严格报告头 + 最新匹配胜出；第三方同前缀评论不劫持。
func TestPostSummary_ScanStrictHeader(t *testing.T) {
	fp := newFakePlatform()
	fp.comments = []*provider.IssueComment{
		{ID: 10, Body: "<!-- argus:report task_id=other head=x ver=v1 -->\n旧", Author: &provider.CRUser{Username: "bot"}},
		{ID: 11, Body: "普通评论 <!-- argus:report task_id=t head=s ver=v1 -->", Author: &provider.CRUser{Username: "bot"}}, // 不独占一行
		{ID: 12, Body: "<!-- argus:report task_id=t head=s ver=v1 -->\n好", Author: &provider.CRUser{Username: "bot"}},
		{ID: 13, Body: "<!-- argus:report task_id=t2 head=s ver=v1 -->\n更新", Author: &provider.CRUser{Username: "bot"}},
	}
	p, _ := newTestPoster(t, fp)
	id, err := p.postSummary(context.Background(), mkPR(), "## 扫描更新")
	if err != nil {
		t.Fatal(err)
	}
	if id != 13 {
		t.Fatalf("应取最后一条严格头匹配: id=%d", id)
	}
	if fp.updated[13] != "## 扫描更新" {
		t.Fatalf("应更新 13: %v", fp.updated)
	}
}

// BotUsername 作者校验：他人同头评论不劫持。
func TestPostSummary_ScanBotAuthorFilter(t *testing.T) {
	fp := newFakePlatform()
	fp.comments = []*provider.IssueComment{
		{ID: 20, Body: "<!-- argus:report task_id=t head=s ver=v1 -->\n劫持", Author: &provider.CRUser{Username: "attacker"}},
		{ID: 21, Body: "<!-- argus:report task_id=t head=s ver=v1 -->\n本尊", Author: &provider.CRUser{Username: "argus-bot"}},
	}
	p, _ := newTestPoster(t, fp)
	p.BotUsername = "argus-bot"
	id, err := p.postSummary(context.Background(), mkPR(), "## 作者过滤")
	if err != nil {
		t.Fatal(err)
	}
	if id != 21 {
		t.Fatalf("应命中 bot 自己的评论: id=%d", id)
	}
}

// 长讨论串尾部的报告评论（旧「第一页」边界之外）仍应被扫描命中并原地更新——
// SDK 全分页收齐后扫描步无位置盲区，勿在调用侧加早停/截断。
func TestPostSummary_ScanFindsLateComment(t *testing.T) {
	fp := newFakePlatform()
	for i := range 60 {
		fp.comments = append(fp.comments, &provider.IssueComment{
			ID: int64(100 + i), Body: fmt.Sprintf("讨论 %d", i),
			Author: &provider.CRUser{Username: "dev"},
		})
	}
	// 报告评论位于讨论串末尾（ID 最大=最新），旧实现只扫首页时不可见
	fp.comments = append(fp.comments, &provider.IssueComment{
		ID: 199, Body: "<!-- argus:report task_id=t head=s ver=v1 -->\n晚到报告",
		Author: &provider.CRUser{Username: "argus-bot"},
	})
	p, _ := newTestPoster(t, fp)
	id, err := p.postSummary(context.Background(), mkPR(), "## 全量扫描更新")
	if err != nil {
		t.Fatal(err)
	}
	if id != 199 {
		t.Fatalf("应命中串尾报告评论并原地更新: id=%d", id)
	}
	if fp.updated[199] != "## 全量扫描更新" {
		t.Fatalf("应更新 199: %v", fp.updated)
	}
	if fp.createNoteN != 0 {
		t.Fatalf("命中即不新建: %d", fp.createNoteN)
	}
}

// 历史缓存兜底：KnownCommentIDs 直连更新（①②不中时），新→旧遍历、失败跳过。
func TestPostSummary_KnownCommentsFallback(t *testing.T) {
	fp := newFakePlatform()
	// ① 无 last_summary_comment_id；② 扫描不中（评论不在第一页/无严格头）
	p, store := newTestPoster(t, fp)
	for _, id := range []int64{30, 31, 32} {
		if err := store.AppendReviewTargetComment(context.Background(), "gitea", "acme", "app", "7", id); err != nil {
			t.Fatal(err)
		}
	}
	// 缓存新→旧：先试 32（失败→v3.91.2 换新成功）——换新直接命中，31/30 不再触达
	fp.updateErrs[32] = fmt.Errorf("500")
	id, err := p.postSummary(context.Background(), mkPR(), "## 缓存兜底")
	if err != nil {
		t.Fatal(err)
	}
	if id == 32 || id == 0 {
		t.Fatalf("32 换新成功应返回新 ID: %d", id)
	}
	if _, tried := fp.updated[31]; tried {
		t.Fatal("32 换新成功后不应再试 31")
	}
	if _, tried := fp.updated[30]; tried {
		t.Fatal("32 换新成功后不应再试更旧的 30")
	}
	for _, c := range fp.comments {
		if c.ID == 32 {
			t.Fatalf("旧评论 32 应被换新删除: %+v", fp.comments)
		}
	}
}

// 扫描失败（SDK 报错）→ 降级新建，不挂死。
func TestPostSummary_ScanErrorCreates(t *testing.T) {
	fp := newFakePlatform()
	fp.listErr = fmt.Errorf("list boom")
	p, _ := newTestPoster(t, fp)
	id, err := p.postSummary(context.Background(), mkPR(), "## 新建")
	if err != nil || id == 0 {
		t.Fatalf("扫描失败应新建: id=%d err=%v", id, err)
	}
	if fp.createNoteN != 1 {
		t.Fatalf("应新建一条: %d", fp.createNoteN)
	}
}

// ---------- v5.4.7 总结评论轮换浮顶 ----------

// head 推进（新一轮评审）→ 删旧建新浮顶，定位缓存回写新 ID + 新 head。
func TestPostSummary_RotatesOnNewHead(t *testing.T) {
	fp := newFakePlatform()
	fp.comments = append(fp.comments, &provider.IssueComment{ID: 42, Body: "旧报告", CreatedAt: time.Now()})
	p, store := newTestPoster(t, fp)
	if err := store.UpsertReviewTarget(context.Background(), "gitea", "acme", "app", "7", 42, "sha-1"); err != nil {
		t.Fatal(err)
	}
	pr := mkPR()
	pr.HeadSHA = "sha-2"
	id, err := p.postSummary(context.Background(), pr, "## 新一轮报告")
	if err != nil {
		t.Fatal(err)
	}
	if id == 42 {
		t.Fatalf("head 推进应新建评论（浮顶），不应复用旧 ID: %d", id)
	}
	if len(fp.comments) != 1 || fp.comments[0].ID != id {
		t.Fatalf("旧评论应被删除、仅剩新建评论: %+v", fp.comments)
	}
	tgt, _ := store.GetReviewTarget(context.Background(), "gitea", "acme", "app", "7")
	if tgt == nil || tgt.LastSummaryCommentID != id || tgt.LastHeadSHA != "sha-2" {
		t.Fatalf("定位缓存应回写新 ID+新 head: %+v", tgt)
	}
}

// 同 head（重试/幂等刷新）→ 原地更新，不删不建。
func TestPostSummary_SameHeadUpdatesInPlace(t *testing.T) {
	fp := newFakePlatform()
	p, store := newTestPoster(t, fp)
	if err := store.UpsertReviewTarget(context.Background(), "gitea", "acme", "app", "7", 42, "sha-1"); err != nil {
		t.Fatal(err)
	}
	id, err := p.postSummary(context.Background(), mkPR(), "## 刷新")
	if err != nil || id != 42 {
		t.Fatalf("同 head 应原地更新: id=%d err=%v", id, err)
	}
	if fp.createNoteN != 0 {
		t.Fatalf("不应新建: %d", fp.createNoteN)
	}
	if fp.updated[42] != "## 刷新" {
		t.Fatalf("应原地更新评论 42: %v", fp.updated)
	}
}

// 删除失败 → 降级原地更新，回帖主链不阻断。
func TestPostSummary_RotateDeleteFailsFallsBack(t *testing.T) {
	fp := newFakePlatform()
	fp.deleteErr = fmt.Errorf("permission denied")
	p, store := newTestPoster(t, fp)
	if err := store.UpsertReviewTarget(context.Background(), "gitea", "acme", "app", "7", 42, "sha-1"); err != nil {
		t.Fatal(err)
	}
	pr := mkPR()
	pr.HeadSHA = "sha-2"
	id, err := p.postSummary(context.Background(), pr, "## 降级更新")
	if err != nil || id != 42 {
		t.Fatalf("删除失败应降级原地更新: id=%d err=%v", id, err)
	}
	if fp.updated[42] != "## 降级更新" {
		t.Fatalf("应原地更新评论 42: %v", fp.updated)
	}
}
