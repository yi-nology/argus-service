// runner_workflow.go —— 工作流装配面：技能文本注入 + Workflow 构造/工厂（阶段模型/预接线）。

package runner

import (
	"context"

	"github.com/oagentkit/llm"

	"github.com/yi-nology/argus/internal/plugin"
	"github.com/yi-nology/argus/internal/workflow/requirement"
)

// skillText 惰性解析 skillRef（§9.1：含 checksum，防内容漂移难排查），10 分钟周期刷新。
// 改 skill 文件后无需重启，下一个任务自动加载新内容。
// 锁只护缓存字段；SkillProvider I/O 在锁外执行，避免多任务 R3 装配互相阻塞。
func (r *Runner) skillText(_ context.Context) string {
	return skillText(r)
}

// loadSkillText 实际解析 skill 内容（无锁；结果由 skillText 写回缓存）。
func (r *Runner) loadSkillText(bgCtx context.Context) string {
	return loadSkillText(r, bgCtx)
}

// WorkflowFor 导出每任务 Workflow 构造（ReviewTools 钩子复用 R3 的工具基础设施；
// 构造成本可忽略——字段全部共享引用）。
func (r *Runner) WorkflowFor(budget *llm.Budget) *requirement.Workflow {
	return workflowFor(r, budget)
}

// WorkflowFactory 每任务构造 Workflow（供 requirement-compliance 插件与 runner 共用）。
// budget 决定该任务 LLM 客户端的预算累计器；nil = 不限（协议重入路径）。
// 契约类型用 plugin.WorkflowFactory（v3.5.6 上提）——编排层不再 import 具体适配器。
func (r *Runner) WorkflowFactory() plugin.WorkflowFactory {
	return workflowFactory(r)
}

// buildWorkflow 每任务构造 Workflow 实例：LLM 客户端持有任务预算累计器，
// 其余字段全部共享引用，构造成本可忽略。
func (r *Runner) buildWorkflow(budget *llm.Budget) *requirement.Workflow {
	return buildWorkflow(r, budget)
}
