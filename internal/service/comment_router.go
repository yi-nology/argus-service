// comment_router.go —— @argus 评论意图路由（v3.4，agentkit v0.8.0 router 架构原语）。
//
// 背景：P5 之后所有 @argus 自由文本评论一律进 Q&A ReAct——"请重新审查"这类
// 重审指令也被当成问题回答，作者只能推新提交触发 webhook。router 把可枚举的
// 入口意图分类后分派到差异化的处理链（agentkit README "Router+Skill 该用"：
// 意图可枚举、处理链差异大、分类错误有 Fallback 兜底）：
//
//	review  → 建全量复审任务（CreateTaskDirect，无 head-SHA 去重），回执评论
//	   意图 → Q&A（P5 路径原样保留；分类失败/低置信的兜底也是它）
//
// 纪律：
//   - 默认关闭（ARGUS_COMMENT_ROUTER=1 开启）——重审是会烧预算的强动作，
//     显式 opt-in 而非默认行为变更；
//   - 确定性优先：短命令（≤30 rune）命中重审关键词直接直达，零分类调用；
//     LLM 分类只裁不确定的歧义文本（成本纪律）；
//   - MinConfidence 之下的 review 视为 ask——误触发重审的代价远高于多答一句；
//   - 分类模型（ARGUS_ROUTER_MODEL）用快模型即可，一次 GenerateJSON 调用。

package service

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/cloudwego/eino/components/model"

	"github.com/oagentkit/agent/router"
)

// 评论意图词表（router.Route.Name；词表保持最小——两类处理链，第三类就是过度设计）。
const (
	commentIntentReview = "review"
	commentIntentAsk    = "ask"
)

// reviewKeywordMaxRunes 关键词快路径的长度上限：超过视为歧义长句，交 LLM 分类。
// "这个重新审查的逻辑对吗"这类长句不会被误触发重审。
const reviewKeywordMaxRunes = 30

// reviewKeywords 重审意图的显式关键词（小写包含匹配）。
var reviewKeywords = []string{
	"重新审查", "重新评审", "再审", "重审", "重跑评审", "rerun", "re-review", "recheck",
	"评审一下", "帮我评审", "请评审", "复核一下", "帮我复核", "请复核",
}

// dispatchReview 显式重审命令（review/评审/复核…）：确定性触发全量复审任务，
// 免关键词快路径与 LLM 意图分类。支持 `--template <key>` 任务级模板覆盖（v6.11.0：
// 不动仓配置即可试用 analyst/minimal）。返回 true 表示已消费该评论。
func (s *Service) dispatchReview(ctx context.Context, question, platform, owner, repo,
	prNum, prRef, delivery string) bool {
	return dispatchReview(s, ctx, question, platform, owner, repo, prNum, prRef, delivery)
}

// questionMarkers 疑问标记（小写包含匹配）：出现即不走关键词快路径。
// "这个重新审查的逻辑对吗"（11 rune）不含否定词、长度也在阈值内，原本会命中
// "重新审查"直接触发全量重审（深挖 P0）——它是问题不是指令，交 LLM 意图分类
// 裁决（MinConfidence 之下回落 ask，不烧重审预算）。
var questionMarkers = []string{"吗", "？", "?", "为什么", "怎么", "怎样", "如何", "什么", "难道"}

// matchReviewKeyword 短命令关键词快路径判定（纯函数）。
// agentkit router.KeywordHit：子串 + 否定前置守门（「不要重新审查」不命中）；
// 拉丁词走词边界（rerun 不误命中 rerunning 内部构式外的更长词）；
// 疑问标记守门（问句交 LLM 分类）。
func matchReviewKeyword(question string) bool {
	if utf8.RuneCountInString(question) > reviewKeywordMaxRunes {
		return false
	}
	lower := strings.ToLower(question)
	for _, m := range questionMarkers {
		if strings.Contains(lower, m) {
			return false
		}
	}
	for _, kw := range reviewKeywords {
		// 中文关键词：否定守门；拉丁关键词：再加词边界
		if isASCIIKeyword(kw) {
			if router.KeywordHitBoundary(lower, kw) {
				return true
			}
			continue
		}
		if router.KeywordHit(lower, kw) {
			return true
		}
	}
	return false
}

// isASCIIKeyword 词表项是否纯 ASCII（拉丁词走边界匹配）。
func isASCIIKeyword(kw string) bool {
	for i := range len(kw) {
		if kw[i] >= 0x80 {
			return false
		}
	}
	return true
}

// routeAndReply 评论意图路由 + 分派执行，返回完整回帖正文。
// 返回 error 仅表示该意图的处理链失败（与既有 Q&A 失败路径同语义：回帖错误说明）。
func (s *Service) routeAndReply(ctx context.Context, platform, owner, repo, prNum, prRef,
	question, delivery string) (string, error) {
	return routeAndReply(s, ctx, platform, owner, repo, prNum, prRef, question, delivery)
}

// classifyIntent LLM 意图分类（不执行处理链）：router.Classify 产出可观测决策
// （route/confidence/reason），MinConfidence 之下的 review 与未知意图一律视为 ask。
// Handle 为 router.New 校验所需占位——分派在 routeAndReply 显式进行，因为回帖
// 格式携带 delivery 幂等标记，必须由持有该上下文的调用方组装。
func (s *Service) classifyIntent(ctx context.Context, question string) (string, router.Decision, error) {
	return classifyIntent(s, ctx, question)
}

// rerunReviewReply review 意图处理链：建全量复审任务 + 回执评论。
// CreateTaskDirect 无同 SHA 去重——重审语义就是"同 head 再来一轮"（Merger 的
// 跨轮指纹/抑制照常生效）。失败返回 error，由调用方走错误回帖路径。
func (s *Service) rerunReviewReply(ctx context.Context, platform, owner, repo, prNum, prRef,
	question, delivery, templateKey string) (string, error) {
	return rerunReviewReply(s, ctx, platform, owner, repo, prNum, prRef, question, delivery, templateKey)
}

// routerChatModel 路由分类模型：ARGUS_ROUTER_MODEL 覆盖 → QA 专用模型 → 主模型。
func (s *Service) routerChatModel() model.BaseChatModel { return routerChatModel(s) }

// replyModel 写作面命令（describe/testgen）回帖模型：QA 专用模型优先，主模型兜底。
func (s *Service) replyModel() model.BaseChatModel { return replyModel(s) }
