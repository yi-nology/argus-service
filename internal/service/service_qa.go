// service_qa.go —— Q&A 引擎装配与追问面（自 service.go 拆出）。

package service

import (
	"context"

	"github.com/yi-nology/argus/internal/memorykernel"
	"github.com/yi-nology/argus/internal/qa"
)

// qa 组装问答引擎（v3.5.6 Q&A 逻辑拆至 internal/qa）。Engine 无自有状态，
// 字段全为共享引用；会话历史在 Store，节流与 delivery 幂等留在本层 HandleComment。
// 单例缓存（qaOnce）：此前每次问答都重建 Engine，纯分配浪费；并发首建安全。
func (s *Service) qa() *qa.Engine {
	s.qaOnce.Do(func() {
		e := &qa.Engine{
			SDK:       s.SDK,
			Store:     s.Store,
			Knowledge: s.Knowledge,
			Model:     s.Model,
			QAModel:   s.QAModel,
			Log:       s.log(),
			// search_code 全文检索（v3.91.0）：QA 与审查面共享工作副本池，
			// ARGUS_CODESEARCH 同源开关
			WC:         s.WC,
			CodeSearch: s.Cfg != nil && s.Cfg.CodeSearch,
			// search_sem 语义检索（v3.126.0）：底座由 bootstrap 双门构建，
			// 配置开关在此同源判定
			SemSearch:   s.SemSearch,
			SemSearchOn: s.Cfg != nil && s.Cfg.SemSearch,
		}
		if s.Cfg != nil {
			e.HistoryBudgetTokens = s.Cfg.QAHistoryBudget
			e.PTCEnabled = s.Cfg.QAProgTools // v3.158.0 ARGUS_QA_PTC opt-in
		}
		// 本仓评审记忆索引（v3.39.0）：Q&A 作答可参考既有 dream 结论。
		// v5.0.0 文件面退役：内核是唯一路径，未装配 = 不挂注入面。
		if s.MemKernel != nil {
			k := s.MemKernel
			e.MemoryIndexFor = func(platform, owner, repo string) string {
				ref := memorykernel.Ref{Platform: platform, Owner: owner, Repo: repo}
				return k.RecallIndex(context.Background(), memorykernel.NamespaceOf(ref),
					memorykernel.OrgNamespace, "", "", 0)
			}
		}
		s.qaEng = e
	})
	return s.qaEng
}

// LatestReview 某 PR 最近一次审查结论（mcpserver ask_argus 数据源）。
// 返回 {task_id, finished_at, verdict, findings, report}；无记录返回 (nil, false)。
func (s *Service) LatestReview(ctx context.Context, platform, owner, repo, number string) (map[string]any, bool) {
	if !s.repoAllowedFor(ctx, platform, owner, repo) {
		return map[string]any{"error": errRepoDenied}, true // 拒绝以内容态可见，不伪装成"无记录"
	}
	return s.qa().LatestReview(ctx, platform, owner, repo, number)
}

// AnswerAboutReview 基于最近一次报告回答追问（ask_argus 带 question 时走这里；
// 单轮直调——报告已在手，无需工具链）。模型未配置或无报告返回 ("" , false)。
func (s *Service) AnswerAboutReview(ctx context.Context, platform, owner, repo, number, question string) (string, bool) {
	if !s.repoAllowedFor(ctx, platform, owner, repo) {
		return errRepoDenied, true // 拒绝以答案态可见，不伪装成"无法回答"
	}
	return s.qa().AnswerAboutReview(ctx, platform, owner, repo, number, question)
}
