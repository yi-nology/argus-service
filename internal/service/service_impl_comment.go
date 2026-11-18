// service_impl_comment.go —— Service 方法体的包级实现（转发壳见原文件）。

package service

import (
	"context"
	"fmt"
	"time"

	"github.com/oagentkit/rag"
	datacache "github.com/oekit/data/cache"
	argus "github.com/yi-nology/argus/biz/model/argus"
	"github.com/yi-nology/argus/internal/commentcmd"
	"github.com/yi-nology/argus/internal/obs"
	"github.com/yi-nology/go-git-platform/model"
)

// admitComment 的包级实现（转发壳见原文件）。
func admitComment(s *Service, ctx context.Context, ev *model.NormalizedEvent,
	delivery string) (commentcmd.Meta, bool) {
	if s.Model == nil {
		s.log().Warn("argus.qa.no_model", "hint", "LLM 未配置，Q&A 评论忽略")
		return commentcmd.Meta{}, false
	}
	commentcmd.NormalizeCommentEvent(ev)
	meta, ok := commentcmd.ParseCommentMeta(ev)
	if !ok {
		return commentcmd.Meta{}, false // 解析失败或非 PR 评论（系统注释/commit/普通 issue）
	}
	// bot 自身评论防循环（Info 留痕：静默丢弃曾让评论链路排查无从下手——
	// "事件到没到、进了哪条路" 全靠推断）。
	if ev.Actor != nil && ev.Actor.Username != "" && ev.Actor.Username == s.botUsernameOf(ctx) {
		s.log().Info("argus.comment.self_skipped", "author", ev.Actor.Username)
		return commentcmd.Meta{}, false
	}
	if delivery != "" {
		fresh, err := s.Store.MarkCommentDelivery(ctx, delivery, time.Now())
		if err != nil {
			s.log().Warn("argus.qa.delivery_mark_failed", "delivery", delivery, "error", err.Error())
		} else if !fresh {
			obs.IncrCommentRedelivery() // P2-05（#16）：平台重投递命中幂等账本
			return commentcmd.Meta{}, false
		}
	}
	if ev.Repo == nil {
		return commentcmd.Meta{}, false
	}
	return meta, true
}

// dispatchSuppression 的包级实现（转发壳见原文件）。
func dispatchSuppression(s *Service, ctx context.Context, ev *model.NormalizedEvent,
	meta commentcmd.Meta, question string) bool {
	// /argus restore fp:xxxxxxxx —— 误抑制恢复（suppress 反向，误杀可撤销）：
	// 将 suppressed 指纹置回 open，后续轮次重新可见。评论轻量路径，不建任务。
	if fp, ok := commentcmd.ExtractRestoreFP(question); ok {
		s.handleRestoreComment(ctx, ev, meta.PRNumber, fp)
		return true
	}
	// /argus suppress fp:xxxxxxxx —— 误报反馈（v3.3 知识飞轮入口）：
	// 抑制该指纹后续轮次 + 结论回流知识库。评论轻量路径，不建任务。
	if fp, ok := commentcmd.ExtractSuppressFP(question); ok {
		s.handleSuppressComment(ctx, ev, meta.PRNumber, fp, meta.Body)
		return true
	}
	return false
}

// dispatchWriting 的包级实现（转发壳见原文件）。
func dispatchWriting(s *Service, ctx context.Context, ev *model.NormalizedEvent,
	question, platform, prNum, prRef, delivery string) bool {
	if hint, ok := commentcmd.ExtractDescribe(question); ok {
		s.handleDescribe(ctx, platform, ev.Repo.Owner, ev.Repo.Name, prNum, prRef, hint, delivery)
		return true
	}
	if hint, ok := commentcmd.ExtractChangelog(question); ok {
		s.handleChangelog(ctx, platform, ev.Repo.Owner, ev.Repo.Name, prNum, prRef, hint, delivery)
		return true
	}
	if target, ok := commentcmd.ExtractTestGen(question); ok {
		s.handleTestGen(ctx, platform, ev.Repo.Owner, ev.Repo.Name, prNum, prRef, target, delivery)
		return true
	}
	// @argus fix fp:短码（v3.85.0）：acpx 编码 agent 沙箱修复，异步执行。
	if fp, ok := commentcmd.ExtractFix(question); ok {
		s.handleFix(ctx, platform, ev.Repo.Owner, ev.Repo.Name, prNum, prRef, fp, delivery)
		return true
	}
	return false
}

// answerComment 的包级实现（转发壳见原文件）。
func answerComment(s *Service, ctx context.Context, ev *model.NormalizedEvent,
	question, platform, prNum, prRef, delivery string) {
	owner, repo := ev.Repo.Owner, ev.Repo.Name
	if !s.Cfg.CommentRouter {
		answer, err := s.qa().Answer(ctx, platform, owner, repo, prNum, prRef, question)
		if err != nil {
			obs.IncrQAFailure("llm")
			s.log().Error("argus.qa.llm_failed", "pr", prRef, "error", err.Error())
			s.postQAError(ctx, platform, owner, repo, prNum, "LLM 调用失败: "+err.Error())
			return
		}
		s.postCommentReply(ctx, platform, owner, repo, prNum, prRef,
			"> "+question+"\n\n"+answer+"\n\n<!-- argus:qa delivery="+delivery+" -->",
			question, len(answer))
		return
	}

	reply, err := s.routeAndReply(ctx, platform, owner, repo, prNum, prRef, question, delivery)
	if err != nil {
		obs.IncrQAFailure("llm")
		s.log().Error("argus.qa.llm_failed", "pr", prRef, "error", err.Error())
		s.postQAError(ctx, platform, owner, repo, prNum, "处理失败: "+err.Error())
		return
	}
	s.postCommentReply(ctx, platform, owner, repo, prNum, prRef, reply, question, len(reply))
}

// handleComment 的包级实现（转发壳见原文件）。
func handleComment(s *Service, ctx context.Context, ev *model.NormalizedEvent, delivery string) {
	meta, ok := s.admitComment(ctx, ev, delivery)
	if !ok {
		return
	}
	ctx = contextWithDeliveryID(ctx, delivery) // P2-03：回帖出口可回填投递账本

	question := commentcmd.ExtractQuestion(meta.Body, s.botUsernameOf(ctx))
	if question == "" {
		return // 不含 @argus 指令，忽略
	}
	if s.dispatchSuppression(ctx, ev, meta, question) {
		return
	}

	prNum := fmt.Sprintf("%d", meta.PRNumber)
	prRef := ev.Repo.Owner + "/" + ev.Repo.Name + "#" + prNum
	platform := string(ev.Source)

	// 信息类指令（help/status）免节流：查询永远畅通，商业产品的"手册随时可查"。
	if s.dispatchInfo(ctx, question, platform, ev.Repo.Owner, ev.Repo.Name, prNum, prRef, delivery) {
		return
	}

	// per-PR 节流（30 秒冷却）。v3.82.0 引入 oekit/data/cache：TTL 过期 + 容量
	// 上限由库接管，取代手写 map+mutex+「超 100 清 1h 前旧条目」驱逐循环。
	// v3.84.0 起实例回归 per-Service（进程级单例曾令 e2e 同进程多 Service
	// 互相污染节流窗口；生产行为不变）。
	// v5.3.0：指令撞冷却不再静默丢弃——回帖告知（有感知），自由文本保持静默防刷屏。
	throttleKey := ev.Repo.Owner + "/" + ev.Repo.Name + "/" + prNum
	if v, _ := s.qaThrottle().Get(ctx, throttleKey); v != nil {
		if commentcmd.MatchAnyCommand(question) {
			s.postQAError(ctx, platform, ev.Repo.Owner, ev.Repo.Name, prNum, commentcmd.ThrottleNoticeText)
		} else {
			s.log().Warn("argus.qa.throttled", "pr", prRef, "question", question)
		}
		return
	}
	_ = s.qaThrottle().Set(ctx, throttleKey, &qaThrottled)

	s.log().Info("argus.qa.received", "pr", prRef, "question", question,
		"actor", ev.Actor.Username, "event_id", delivery)

	// 即时回执 👀（v5.3.0）：评论上先给"已看到"，长任务（重审/复核）不再干等。
	s.ackComment(ctx, platform, ev.Repo.Owner, ev.Repo.Name, meta.CommentID)

	// 烧预算指令门控（v5.3.0）：review/recheck/fix/testgen 需写权限，防路人白烧。
	if commentcmd.IsDriveCommand(question) && !s.actorCanDrive(ctx, platform, ev.Repo.Owner, ev.Repo.Name, actorName(ev)) {
		s.postQAError(ctx, platform, ev.Repo.Owner, ev.Repo.Name, prNum, commentcmd.GateDeniedText)
		return
	}

	// 显式命令优先于自由文本路由：复核（单条 fp）→ 重审（全量）→ 写作/修复 →
	// Q&A/意图分类。确定性命令零 LLM 分类开销，也不受 ARGUS_COMMENT_ROUTER 开关影响。
	if s.dispatchRecheck(ctx, question, platform, ev.Repo.Owner, ev.Repo.Name, prNum, prRef, delivery) {
		return
	}
	if s.dispatchReview(ctx, question, platform, ev.Repo.Owner, ev.Repo.Name, prNum, prRef, delivery) {
		return
	}
	if s.dispatchWriting(ctx, ev, question, platform, prNum, prRef, delivery) {
		return
	}
	s.answerComment(ctx, ev, question, platform, prNum, prRef, delivery)
}

// postCommentReply 的包级实现（转发壳见原文件）。
func postCommentReply(s *Service, ctx context.Context, platform, owner, repo, prNum, prRef,
	body, question string, answerLen int) {
	ic, err := s.SDK.CreateIssueComment(ctx, platform, owner, repo, prNum, body)
	if err != nil {
		obs.IncrCommentPostFailure("reply")
		s.log().Error("argus.qa.reply_failed", "pr", prRef, "error", err.Error())
		s.publishCommentReplied("error", platform, owner, repo, prNum)
		s.markDeliveryOutcome(ctx, deliveryIDFromContext(ctx), "error", 0, prRef)
		return
	}
	var commentID int64
	if ic != nil {
		commentID = ic.ID
	}
	s.markDeliveryOutcome(ctx, deliveryIDFromContext(ctx), "ok", commentID, prRef)
	s.publishCommentReplied("ok", platform, owner, repo, prNum)
	s.log().Info("argus.qa.replied", "pr", prRef, "answer_len", answerLen)
	s.audit(ctx, "qa_reply", "pr", prRef, "question", question, "answer_len", answerLen)
}

// markDeliveryOutcome 的包级实现（转发壳见原文件）。
func markDeliveryOutcome(s *Service, ctx context.Context, delivery, status string, commentID int64, ref string) {
	if s.Store == nil || delivery == "" {
		return
	}
	if err := s.Store.MarkCommentDeliveryOutcome(ctx, delivery, status, commentID, ref); err != nil {
		s.log().Warn("argus.qa.delivery_outcome_failed", "delivery", delivery, "error", err.Error())
	}
}

// handleSuppressComment 的包级实现（转发壳见原文件）。
func handleSuppressComment(s *Service, ctx context.Context, ev *model.NormalizedEvent,
	number int, fpPrefix, commentBody string) {
	platform := string(ev.Source)
	num := fmt.Sprintf("%d", number)
	req := &argus.SuppressFindingRequest{
		Platform: platform, Owner: ev.Repo.Owner, Repo: ev.Repo.Name, Number: num,
		Fingerprint: strPtr(fpPrefix),
		Comment:     strPtr(commentBody),
	}
	resp, status := s.SuppressFinding(ctx, req)
	if resp == nil {
		s.log().Warn("argus.feedback.comment_failed", "pr", ev.Repo.Owner+"/"+ev.Repo.Name+"#"+num, "status", status)
		return
	}
	reply := "已收到误报反馈。"
	if !resp.Matched {
		reply = "未找到匹配的意见指纹，请核对报告中 `fp:` 开头的短码后重试。"
	} else {
		reply = "已标记为误报：后续轮次自动抑制" + s.knowledgeSuffix() + "。"
	}
	s.postQAError(ctx, platform, ev.Repo.Owner, ev.Repo.Name, num, reply)
}

// knowledgeSuffix 的包级实现（转发壳见原文件）。
func knowledgeSuffix(s *Service) string {
	if _, ok := s.Knowledge.(*rag.MilvusStore); ok {
		return "，误报结论已回流知识库（同型问题检索时将带历史语境）"
	}
	return ""
}

// postQAError 的包级实现（转发壳见原文件）。
func postQAError(s *Service, ctx context.Context, platform, owner, repo, number, msg string) {
	body := ":warning: **Argus Q&A 错误**: " + msg +
		"\n\n<!-- argus:qa error -->"
	ic, err := s.SDK.CreateIssueComment(ctx, platform, owner, repo, number, body)
	if err != nil {
		s.log().Error("argus.qa.error_reply_failed", "error", err.Error())
		s.markDeliveryOutcome(ctx, deliveryIDFromContext(ctx), "error", 0, "")
	} else {
		var commentID int64
		if ic != nil {
			commentID = ic.ID
		}
		s.markDeliveryOutcome(ctx, deliveryIDFromContext(ctx), "ok", commentID, "")
	}
	s.publishCommentReplied("error", platform, owner, repo, number)
}

// handleRestoreComment 的包级实现（转发壳见原文件）。
func handleRestoreComment(s *Service, ctx context.Context, ev *model.NormalizedEvent,
	number int, fpPrefix string) {
	platform := string(ev.Source)
	num := fmt.Sprintf("%d", number)
	matched, err := s.restoreFindingCore(ctx, platform, ev.Repo.Owner, ev.Repo.Name, num, fpPrefix)
	if err != nil {
		s.log().Warn("argus.feedback.restore_failed", "pr", ev.Repo.Owner+"/"+ev.Repo.Name+"#"+num, "err", err)
		return
	}
	reply := "已恢复：该意见后续轮次重新可见。"
	if !matched {
		reply = "未找到匹配的已抑制指纹（或短码有歧义），请核对报告中 `fp:` 开头的短码后重试。"
	}
	s.postQAError(ctx, platform, ev.Repo.Owner, ev.Repo.Name, num, reply)
}

// qaThrottle 的包级实现（转发壳见原文件）。
func qaThrottle(s *Service) *datacache.LocalCache[bool] {
	s.qaThrottleMu.Lock()
	defer s.qaThrottleMu.Unlock()
	if s.qaThrottleCch == nil {
		s.qaThrottleCch = datacache.NewLocalCache(datacache.LocalConfig[bool]{
			TTL:     30 * time.Second,
			MaxSize: 200,
		})
	}
	return s.qaThrottleCch
}
