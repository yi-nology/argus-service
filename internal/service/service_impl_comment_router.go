// service_impl_comment_router.go —— Service 方法体的包级实现（转发壳见原文件）。

package service

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/cloudwego/eino/components/model"
	"github.com/oagentkit/agent/router"
	"github.com/yi-nology/argus/internal/commentcmd"
	"github.com/yi-nology/argus/internal/domain"
	"github.com/yi-nology/argus/internal/plugin"
	"github.com/yi-nology/argus/internal/reporttmpl"
)

// dispatchReview 的包级实现（转发壳见原文件）。
func dispatchReview(s *Service, ctx context.Context, question, platform, owner, repo,
	prNum, prRef, delivery string) bool {
	note, ok := commentcmd.ExtractReviewCmd(question)
	if !ok {
		return false
	}
	// --template 旗标：词表校验失败/旗标 malformed 都截停报错（不吞意图——
	// 用户显式给了旗标，静默忽略比报错更糟）。
	tmpl, noteRemainder, found, bad := commentcmd.ExtractTemplateFlag(note)
	switch {
	case bad:
		s.postQAError(ctx, platform, owner, repo, prNum,
			"`--template` 旗标格式有误（用法：`@argus review --template analyst`，可选值 standard|analyst|minimal）")
		return true
	case found && !reporttmpl.IsValid(tmpl):
		s.postQAError(ctx, platform, owner, repo, prNum,
			"未知报告模板 `"+tmpl+"`（可选值："+strings.Join(reporttmpl.Keys(), "|")+"）")
		return true
	}
	if note == "" {
		note = question
	} else if found {
		note = noteRemainder // 旗标剥除后的剩余说明进补充指令
		if note == "" {
			note = question
		}
	}
	reply, err := s.rerunReviewReply(ctx, platform, owner, repo, prNum, prRef, note, delivery, tmpl)
	if err != nil {
		s.log().Error("argus.comment.review_cmd_failed", "pr", prRef, "error", err.Error())
		s.postQAError(ctx, platform, owner, repo, prNum, "处理失败: "+err.Error())
		return true
	}
	s.postCommentReply(ctx, platform, owner, repo, prNum, prRef, reply, note, len(reply))
	return true
}

// routeAndReply 的包级实现（转发壳见原文件）。
func routeAndReply(s *Service, ctx context.Context, platform, owner, repo, prNum, prRef,
	question, delivery string) (string, error) {

	askHandle := func(input string) (string, error) {
		ans, err := s.qa().Answer(ctx, platform, owner, repo, prNum, prRef, input)
		if err != nil {
			return "", err
		}
		// 回帖格式：引用原评论 + delivery 幂等标记（与 v3.3 直答路径完全一致）
		return "> " + input + "\n\n" + ans + "\n\n<!-- argus:qa delivery=" + delivery + " -->", nil
	}
	reviewHandle := func(input string) (string, error) {
		return s.rerunReviewReply(ctx, platform, owner, repo, prNum, prRef, input, delivery, "")
	}

	// 确定性关键词快路径：短命令直达，不花分类调用
	if matchReviewKeyword(question) {
		s.log().Info("argus.comment.route", "pr", prRef, "route", commentIntentReview,
			"via", "keyword", "confidence", 1)
		plugin.ReportCommentRoute(commentIntentReview, "keyword")
		return reviewHandle(question)
	}

	// 歧义文本 → LLM 意图分类；分类失败不中断——回落 Q&A（宁可多答，不因路由故障丢响应）
	intent, d, err := s.classifyIntent(ctx, question)
	if err != nil {
		s.log().Warn("argus.comment.route_fallback", "pr", prRef,
			"reason", err.Error(), "fallback", commentIntentAsk)
		return askHandle(question)
	}
	s.log().Info("argus.comment.route", "pr", prRef, "route", intent,
		"confidence", d.Confidence, "reason", d.Reason)
	plugin.ReportCommentRoute(intent, "llm")
	if intent == commentIntentReview {
		return reviewHandle(question)
	}
	return askHandle(question)
}

// classifyIntent 的包级实现（转发壳见原文件）。
func classifyIntent(s *Service, ctx context.Context, question string) (string, router.Decision, error) {
	r, err := router.New(&router.Config{
		Model:         s.routerChatModel(),
		ModelName:     "argus-comment-router",
		MinConfidence: s.Cfg.RouterMinConfidence,
		Routes: []router.Route{
			{Name: commentIntentReview,
				Description: "要求重新审查/重跑评审/再审一遍（触发新一轮代码评审任务）",
				Handle:      func(context.Context, string) (string, error) { return "", nil }},
			{Name: commentIntentAsk,
				Description: "提问、追问、讨论：想了解代码行为、审查结论或技术问题（对话式回答，不触发评审）",
				Handle:      func(context.Context, string) (string, error) { return "", nil }},
		},
	})
	if err != nil {
		return "", router.Decision{}, fmt.Errorf("路由构造失败: %w", err)
	}
	d, err := r.Classify(ctx, question)
	if err != nil {
		if d.Route == "" {
			return "", d, fmt.Errorf("意图分类失败: %w", err)
		}
		// agentkit v0.9.x 起 router.Classify 自守 MinConfidence/词表门槛：低置信
		// 与未知意图以 error 返回，但 Decision 仍带回 route/confidence 可观测。
		// 这里保持既有语义——低置信一律回落 ask，不把"不确定"冒充成硬故障。
		s.log().Info("argus.comment.router.fallback", "route", d.Route,
			"confidence", d.Confidence, "reason", d.Reason)
		return commentIntentAsk, d, nil
	}
	if d.Route == commentIntentReview && d.Confidence < s.Cfg.RouterMinConfidence {
		return commentIntentAsk, d, nil
	}
	if d.Route != commentIntentReview {
		return commentIntentAsk, d, nil // ask / none / 未知词表 → ask
	}
	return commentIntentReview, d, nil
}

// rerunReviewReply 的包级实现（转发壳见原文件）。
func rerunReviewReply(s *Service, ctx context.Context, platform, owner, repo, prNum, prRef,
	question, delivery, templateKey string) (string, error) {
	artifacts := map[string]string{plugin.ArtifactTrigger: "comment"}
	// v6.11.0 任务级模板覆盖：commentcmd 已做词表校验，此处直注任务 Artifacts
	//（runner 侧仍做 IsValid 防御复核——API 建任务路径可绕开 commentcmd）。
	if templateKey != "" {
		artifacts[plugin.ArtifactReportTemplate] = templateKey
	}
	resp, status := s.CreateTaskDirect(ctx, domain.TaskKindRequirementCompliance,
		platform, owner, repo, prNum, artifacts)
	if status != http.StatusOK || resp == nil {
		return "", fmt.Errorf("建审查任务失败（HTTP %d）", status)
	}
	s.log().Info("argus.comment.rerun", "pr", prRef, "task", resp.TaskId, "duplicated", resp.GetDuplicated())
	s.Audit.Log(auditTaskCreate, "task_id", resp.TaskId, "source", "comment:review",
		"owner", owner, "repo", repo, "number", prNum)
	// v3.8.0：命中在飞任务时如实回执，不再谎报"新一轮已开始"
	if resp.GetDuplicated() {
		return "> " + question + "\n\n已收到重新审查请求。该 PR 已有审查在进行中（任务 `" + resp.TaskId +
			"`，未重复建任务），完成后将在本 PR 回帖评审报告。\n\n<!-- argus:review delivery=" + delivery + " -->", nil
	}
	return "> " + question + "\n\n已收到重新审查请求，新一轮全量评审已开始（任务 `" + resp.TaskId +
		"`），完成后将在本 PR 回帖评审报告。\n\n<!-- argus:review delivery=" + delivery + " -->", nil
}

// routerChatModel 的包级实现（转发壳见原文件）。
func routerChatModel(s *Service) model.BaseChatModel {
	if s.RouterModel != nil {
		return s.RouterModel
	}
	if s.QAModel != nil {
		return s.QAModel
	}
	return s.Model
}

// replyModel 的包级实现（转发壳见原文件）。
func replyModel(s *Service) model.BaseChatModel {
	if s.QAModel != nil {
		return s.QAModel
	}
	return s.Model
}
