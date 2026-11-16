// service_agents.go —— Agent 可观测与 MCP 副驾数据面：agent 清单/仓库配置视图/任务阶段/findings/建议定位。

package service

import (
	"context"

	argus "github.com/yi-nology/argus/biz/model/argus"
	"github.com/yi-nology/argus/internal/domain"
	"github.com/yi-nology/argus/internal/plugin"
	"github.com/yi-nology/argus/internal/storage"
)

// ListAgents 插拔层可观测（🔒）：Registry 全部插件清单/状态（含 needs/triggers）。
// platform/owner/repo 三参全传时，附带该仓库 .reviewagents.yaml 的可观测视图：
// 配置原文（信任边界规范化后）+ 各插件生效状态 + 告警（§3.4 落到看得见的地方）。
func (s *Service) ListAgents(ctx context.Context, platform, owner, repo string) *argus.ListAgentsResponse {
	return listAgents(s, ctx, platform, owner, repo)
}

// repoConfigView 拉取并解析仓库配置为可观测视图；失败降级为 loaded=false + 告警（不 5xx）。
func (s *Service) repoConfigView(ctx context.Context, platform, owner, repo string) *argus.RepoConfigView {
	return repoConfigView(s, ctx, platform, owner, repo)
}

// TaskStages 任务阶段时间线（零 LLM、纯 DB 推导）：形态见 deriveStageDone。
// 无任务返回 false。
func (s *Service) TaskStages(ctx context.Context, taskID string) (map[string]any, bool) {
	return taskStages(s, ctx, taskID)
}

// deriveStageDone 阶段完成度推导（v3.132.0 加 stage_progress 标记主判据）：
//   - stage_progress（onStage 每阶段开头落库）：标记之前的阶段全 done——这是
//     主判据，天然涵盖「跳过的阶段」（RunReviewOnly 无 R1/R3/R35，此前被推导
//     成"没做到"，轮询恒显示 R1）；标记本身的阶段=进行中（不算 done）。
//   - 产物规则（老任务无标记时的回退，有标记时亦是佐证）：checkpoint_r1_spec/
//     r2_summary → R1/R2；debug r3_checks/r3_raw → R3；result_json → R4；
//     +verdict → R5。
//   - 任务终态 done → 全链 done（覆盖无产物场景）。
func deriveStageDone(arts, debug map[string]string, resultJSON, verdict, status string) map[string]bool {
	stageDone := map[string]bool{}
	// 主判据：进度标记位置
	if progress := arts[plugin.ArtifactStageProgress]; progress != "" {
		for _, st := range plugin.StageTimeline {
			if st == progress {
				break
			}
			stageDone[st] = true
		}
	}
	// 产物回退/佐证
	if _, ok := arts[plugin.ArtifactCkptR1Spec]; ok {
		stageDone[plugin.LayerR1] = true
	}
	if _, ok := arts[plugin.ArtifactCkptR2Summary]; ok {
		stageDone[plugin.LayerR2] = true
	}
	if _, ok := debug[plugin.DebugKeyR3Checks]; ok {
		stageDone[plugin.LayerR3] = true
	}
	if debug[plugin.DebugKeyR3Raw] != "" {
		stageDone[plugin.LayerR3] = true
	}
	if resultJSON != "" {
		stageDone[plugin.LayerR4] = true
	}
	if resultJSON != "" && verdict != "" {
		stageDone[plugin.LayerR5] = true
	}
	if status == storage.StatusDone {
		for _, st := range plugin.StageTimeline {
			stageDone[st] = true
		}
	}
	return stageDone
}

// Findings 结构化意见清单（fp/suggested_fix/evidence 直出，副驾免解析 markdown）。
func (s *Service) Findings(ctx context.Context, taskID string) ([]map[string]any, bool) {
	return findings(s, ctx, taskID)
}

// findingDTO 单条意见的 MCP/副驾直出载荷（出口面单源，v6.18.0 起含 evidence：
// Finding 级证据随结构化清单直出——schema 有字段而出口不带，消费面等于看不到）。
func findingDTO(f domain.Finding) map[string]any {
	return map[string]any{
		"fp": f.FP, "file": f.File, "line": f.Line, "severity": f.Severity,
		"comment": f.Comment, "by": f.By, "suggested_fix": f.SuggestedFix,
		"evidence": f.Evidence,
		// Phase 2 Evidence v2：服务端核验四态（模型不经 wire 产出；空=未核验）。
		"evidence_status": string(f.EvidenceStatus),
	}
}

// SearchCode MCP search_code 数据面（v3.121.0）：PR 工作副本受控全文检索。
// 启用门控（ARGUS_CODESEARCH/工作副本池）、目录解析与检索语义都在 qa.Engine，
// 门面纯透传——与 LatestReview/AnswerAboutReview 同型。
func (s *Service) SearchCode(ctx context.Context, platform, owner, repo, number, pattern, glob string, ignoreCase bool) (string, error) {
	return searchCode(s, ctx, platform, owner, repo, number, pattern, glob, ignoreCase)
}

// Suggestion 按指纹短码取回单条建议（一键回写的数据源；应用动作在客户端侧执行）。
func (s *Service) Suggestion(ctx context.Context, taskID, fpPrefix string) (map[string]any, bool) {
	return suggestion(s, ctx, taskID, fpPrefix)
}
