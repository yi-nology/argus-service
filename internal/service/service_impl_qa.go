// service_impl_qa.go —— Service 方法体的包级实现（转发壳见原文件）。

package service

import (
	"context"

	"github.com/yi-nology/argus/internal/memorykernel"
	"github.com/yi-nology/argus/internal/plugin"
	"github.com/yi-nology/argus/internal/qa"
	"github.com/yi-nology/argus/internal/tenant"
)

// serviceQA 的包级实现（转发壳见原文件）。
func serviceQA(s *Service) *qa.Engine {
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
			// P3 opt-in（ARGUS_REACT_STREAM）：QA ReAct 流式增量只进本地 SSE
			// 观测面（不落 outbox）；关闭时 DeltaSink nil = 旧整段语义。
			if s.Cfg.ReActStream {
				e.DeltaSink = func(platform, owner, repo, number string, d plugin.AgentDelta) {
					s.publishQADelta(platform, owner, repo, number, d)
				}
			}
		}
		// 本仓评审记忆索引（v3.39.0）：Q&A 作答可参考既有 dream 结论。
		// v5.0.0 文件面退役：内核是唯一路径，未装配 = 不挂注入面。
		if s.MemKernel != nil {
			k := s.MemKernel
			// 租户贯穿（#84）：回调 ctx 携 QA 事件归属租户——记忆索引按租户
			// 命名空间装载，组织桶不再全租户共注。
			e.MemoryIndexFor = func(ctx context.Context, platform, owner, repo string) string {
				ref := memorykernel.Ref{Platform: platform, Owner: owner, Repo: repo, Tenant: tenant.FromContext(ctx)}
				return k.RecallIndex(tenant.NewContext(context.Background(), tenant.FromContext(ctx)),
					memorykernel.NamespaceOf(ref), memorykernel.OrgNamespaceOf(tenant.FromContext(ctx)), "", "", 0)
			}
		}
		s.qaEng = e
	})
	return s.qaEng
}

// latestReview 的包级实现（转发壳见原文件）。
func latestReview(s *Service, ctx context.Context, platform, owner, repo, number string) (map[string]any, bool) {
	if !s.repoAllowedFor(ctx, platform, owner, repo) {
		return map[string]any{"error": errRepoDenied}, true // 拒绝以内容态可见，不伪装成"无记录"
	}
	return s.qa().LatestReview(ctx, platform, owner, repo, number)
}

// answerAboutReview 的包级实现（转发壳见原文件）。
func answerAboutReview(s *Service, ctx context.Context, platform, owner, repo, number, question string) (string, bool) {
	if !s.repoAllowedFor(ctx, platform, owner, repo) {
		return errRepoDenied, true // 拒绝以答案态可见，不伪装成"无法回答"
	}
	return s.qa().AnswerAboutReview(ctx, platform, owner, repo, number, question)
}
