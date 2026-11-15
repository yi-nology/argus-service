// runner_announce.go —— 进度事件、start marker 与终态播报（自 runner_report.go 拆出）。

package runner

import (
	"context"
	"fmt"
	"strings"

	"github.com/yi-nology/argus/internal/plugin"
	"github.com/yi-nology/argus/internal/progress"
	"github.com/yi-nology/argus/internal/storage"
)

// publish 广播进度事件（nil Bus 安全）+ 落 outbox（P1-05，#10 跨实例分发）。
func (r *Runner) publish(typ string, task *storage.Task, status, verdict string) {
	publish(r, typ, task, status, verdict)
}

// publishStage 阶段级进度事件（task.stage；v3.40.0 SSE 阶段可视化数据源）。
func (r *Runner) publishStage(task *storage.Task, stage string) {
	publishStage(r, task, stage)
}

// publishOutbox 进程内 Bus + outbox 双写（与 service.publishProgress 同语义：
// 本实例实时、跨实例 fanout 回灌；追加失败计数+告警不阻断）。
// v6.12 P1：本地发布前登记 EventSeen 环——fanout 回灌同 ID 跳过，同实例
// at-most-once（评估实锤的「同实例收到两次」竞窗修复；nil 时退回旧语义）。
func (r *Runner) publishOutbox(ev progress.Event) {
	publishOutbox(r, ev)
}

// stageCNNames 执行期进度条的阶段中文名（v6.1.5）。键一律引用 plugin.Layer*
// 常量（词表单一出处 plugin/hooks.go StageTimeline），覆盖审查管线全部阶段
// R1–R5；子阶段（R1a）经 stageSlot 前缀回退归到主阶段槽位。
var stageCNNames = map[string]string{
	plugin.LayerR1:  "需求解析",
	plugin.LayerR2:  "变更理解",
	plugin.LayerR3:  "完成度判定",
	plugin.LayerR35: "报告反思",
	plugin.LayerR4:  "专家插件",
	plugin.LayerR5:  "终审裁决",
}

// stageSlot stage 在 StageTimeline 中的槽位：精确匹配优先；未命中回退前缀匹配
// （与 llm.StageRouter 同语义——LayerR1 前缀命中 LayerR1A 子阶段），最长前缀胜出。
// 词表外（含空串）返回 -1。
func stageSlot(stage string) int {
	for i, s := range plugin.StageTimeline {
		if s == stage {
			return i
		}
	}
	best := -1
	for i, s := range plugin.StageTimeline {
		if strings.HasPrefix(stage, s) && (best < 0 || len(s) > len(plugin.StageTimeline[best])) {
			best = i
		}
	}
	return best
}

// stageProgressBar 执行期阶段进度条（v6.1.5）：已过 ✅、当前 ⏳、未到显式
// 「未开始」，顺序即 plugin.StageTimeline 规范序。current 不在词表时全部按
// 「未开始」展示（保守：宁可不声称进度，也不错报完成）。纯函数，独立可测。
func stageProgressBar(current string) string {
	cur := stageSlot(current)
	parts := make([]string, 0, len(plugin.StageTimeline))
	for i, s := range plugin.StageTimeline {
		switch {
		case i == cur:
			parts = append(parts, fmt.Sprintf("%s %s ⏳", s, stageCNNames[s]))
		case cur >= 0 && i < cur:
			parts = append(parts, fmt.Sprintf("%s %s ✅", s, stageCNNames[s]))
		default:
			parts = append(parts, fmt.Sprintf("%s %s 未开始", s, stageCNNames[s]))
		}
	}
	return "进度：" + strings.Join(parts, " · ")
}

// startMarkerProgressBody 执行期 start marker 文案（v6.1.5）：「审查中」+ 阶段
// 进度条 + 幂等锚点。锚点格式与 postStartMarker 初版及三态收口一致，setStartMarker
// 扫锚点原地更新的定位逻辑复用同一条锚点线。
func startMarkerProgressBody(taskID, stage string) string {
	return fmt.Sprintf("🔍 Argus 审查中…\n\n%s\n\n<!-- argus:start task_id=%s -->",
		stageProgressBar(stage), taskID)
}

// postStartMarker 审查开始时在 MR 上留一条"审查中"评论，便于用户感知进度。
// 失败不阻塞审查流程（仅告警）。
// 幂等复用（v3.8.0）：先找最近的 start-marker 评论，存在则原地更新其 task_id
// 而不是新建——此前每次重跑（发布打断→重置→重跑）都留一条孤儿"审查中"。
func (r *Runner) postStartMarker(ctx context.Context, task *storage.Task) {
	postStartMarker(r, ctx, task)
}

// setStartMarker 复用/新建 start marker（生命周期：审查中→完成/重试中/最终失败，
// 三态更新——此前 marker 发布后永不更新，失败后 MR 作者只见「审查中」挂死）。
// 复用最近一条 start marker（含历史任务的）：ListIssueComments 平台侧通常
// 按时间正序，取末尾几条扫描；找不到锚点才新建。
func (r *Runner) setStartMarker(ctx context.Context, task *storage.Task, body string) {
	setStartMarker(r, ctx, task, body)
}

// finish 通知：SSE 终态事件 + octopus IM 推送（各自 nil/未配置安全）。
func (r *Runner) announceTerminal(ctx context.Context, task *storage.Task, status, verdict string, out *plugin.WorkflowOutput, failReason string) {
	announceTerminal(r, ctx, task, status, verdict, out, failReason)
}
