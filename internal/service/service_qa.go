// service_qa.go —— Q&A 引擎装配与追问面（自 service.go 拆出）。

package service

import (
	"context"

	"github.com/yi-nology/argus/internal/qa"
)

// qa 组装问答引擎（v3.5.6 Q&A 逻辑拆至 internal/qa）。Engine 无自有状态，
// 字段全为共享引用；会话历史在 Store，节流与 delivery 幂等留在本层 HandleComment。
// 单例缓存（qaOnce）：此前每次问答都重建 Engine，纯分配浪费；并发首建安全。
func (s *Service) qa() *qa.Engine { return serviceQA(s) }

// LatestReview 某 PR 最近一次审查结论（mcpserver ask_argus 数据源）。
// 返回 {task_id, finished_at, verdict, findings, report}；无记录返回 (nil, false)。
func (s *Service) LatestReview(ctx context.Context, platform, owner, repo, number string) (map[string]any, bool) {
	return latestReview(s, ctx, platform, owner, repo, number)
}

// AnswerAboutReview 基于最近一次报告回答追问（ask_argus 带 question 时走这里；
// 单轮直调——报告已在手，无需工具链）。模型未配置或无报告返回 ("" , false)。
func (s *Service) AnswerAboutReview(ctx context.Context, platform, owner, repo, number, question string) (string, bool) {
	return answerAboutReview(s, ctx, platform, owner, repo, number, question)
}
