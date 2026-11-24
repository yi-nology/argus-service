// compliance.go —— R3 合规判定面：CheckCompliance（ReAct agent）+ 指令构造 + 降级报告 + 运行器。

package requirement

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/components/tool"

	"github.com/yi-nology/argus/internal/plugin"
)

// CheckCompliance R3：完成度判定 → ComplianceReport（ADK ReAct agent + 工具）。
// JSON 解析失败重试 1 次后仍败 → 全部 AC 置 unverifiable（§10.2，结果不丢）。
func (w *Workflow) CheckCompliance(ctx context.Context, task plugin.Task, spec *plugin.RequirementSpec, changeSummary string) (*plugin.ComplianceReport, string, error) {
	return checkCompliance(w, ctx, task, spec, changeSummary)
}

// r3Query R3 用户消息组装：验收清单 + 变更摘要（R2）+ 清洗后 diff + 逐条判定指令
// （各数据段一律走 DataFence 注入面围栏）。
func r3Query(ctx context.Context, task plugin.Task, spec *plugin.RequirementSpec, changeSummary string) string {
	user := strings.Builder{}
	user.WriteString(DataFence(ctx, "验收清单", renderACs(spec)))
	user.WriteString("\n")
	if changeSummary != "" {
		user.WriteString(DataFence(ctx, "变更摘要（R2）", changeSummary))
		user.WriteString("\n")
	}
	if task.Diff != nil {
		user.WriteString(DataFence(ctx, "git diff（已清洗）", task.Diff.Text))
		user.WriteString("\n")
	}
	user.WriteString("\n请逐条判定验收清单完成度并输出 JSON。")
	return user.String()
}

// r3ToolsFactory 工具表经工厂按尝试重建（WithCallLimit 计数不跨重试累计）；
// 构造失败回注告警并降级为空表（agent 裸跑，不中断 ReAct）。
func (w *Workflow) r3ToolsFactory(ctx context.Context, task plugin.Task,
	spec *plugin.RequirementSpec) func() []tool.BaseTool {
	return r3ToolsFactory(w, ctx, task, spec)
}

// r3ParseAligned 解析 + 双向对齐 + 证据门（首试与重试共用）：证据路径核对集
// （深挖修复）以本次变更文件集为参照系——证据引用的路径必须落在其中，编造的
// file:line 才真正过不了门。
func (w *Workflow) r3ParseAligned(spec *plugin.RequirementSpec,
	task plugin.Task) func(final string) (*plugin.ComplianceReport, string, bool) {
	return r3ParseAligned(w, spec, task)
}

// r3RetryOnce 解析失败后的自救重试一轮：预算门（深挖修复；v5.5.4 修正适用域）
// → 带错误回喂重跑 → 再解析；任一环失败返回降级报告（结果不丢）。
func (w *Workflow) r3RetryOnce(ctx context.Context, instruction, query, final string,
	task plugin.Task, spec *plugin.RequirementSpec,
	toolsFactory func() []tool.BaseTool,
	parseAligned func(string) (*plugin.ComplianceReport, string, bool)) (*plugin.ComplianceReport, string) {
	return r3RetryOnce(w, ctx, instruction, query, final, task, spec, toolsFactory, parseAligned)
}

// r3Instruction R3 系统提示词组装：基线 + 工具策略 + codegraph 触达提示 +
// skill 方法论 + 评审记忆 + 仓库规范（各段非空才追加）。
func (w *Workflow) r3Instruction(ctx context.Context, task plugin.Task, strategy string) string {
	return r3Instruction(w, ctx, task, strategy)
}

// r3RuntimeFailPrefix R3 运行中断（执行失败）降级 Detail 的统一前缀（v5.5.3）：
// completeness 据此区分"运行中断（重跑可救）"与"证据确实不足（真不可验证）"，
// 建议文案不再对前者说"拆分 PR"。
const r3RuntimeFailPrefix = plugin.RuntimeFailMark

// errR3RetrySkippedBudget 预算门在真实约束区间内拦掉了自救重试（v5.5.4）：
// 死因是"输出不可解析 + 预算不允许再试"，措辞须给出调预算的路标。
var errR3RetrySkippedBudget = errors.New("输出不可解析且 token 预算耗尽跳过重试")

// r3FailCause 运行失败死因归类（措辞进报告，供人一眼判断可否重跑）。
func r3FailCause(err error) string {
	s := strings.ToLower(err.Error())
	switch {
	case strings.Contains(s, "预算耗尽"):
		return "输出不可解析且预算门拦下重试（可调 repo_settings.task_token_budget / ARGUS_TASK_TOKEN_BUDGET 后重跑）"
	case strings.Contains(s, "429") || strings.Contains(s, "too many requests"):
		return "上游限流 429（瞬时，可重跑）"
	case strings.Contains(s, "max iterations"):
		return "ReAct 迭代上限打满"
	case strings.Contains(s, "timeout") || strings.Contains(s, "deadline exceeded"):
		return "调用超时"
	default:
		return "执行错误"
	}
}

// degradedComplianceReport R3 判定失败的降级报告：全部 AC 置 unverifiable。
// runErr!=nil 为运行中断（死因透传）；nil 为模型输出不可解析。
// v5.7.1 报告降噪：大清单（jianmu MR47 实弹 24-30 条 AC）逐条盖同款长文案
// 是刷屏——运行中断的 Detail 收敛为短后缀（死因在 Summary/completeness
// 建议里说一次），且措辞不再断言"重跑即愈"（持续限流下重跑两连灭的实弹
// 教训：错峰措辞，不断言必然成功）。
func degradedComplianceReport(spec *plugin.RequirementSpec, runErr error) *plugin.ComplianceReport {
	degraded := &plugin.ComplianceReport{}
	detail := "自动判定不可用，请人工确认"
	if runErr != nil {
		cause := r3FailCause(runErr)
		degraded.Summary = fmt.Sprintf("R3 判定失败（%s），已全部降级为需人工确认", cause)
		detail = fmt.Sprintf("%s（%s）：建议错峰重跑", r3RuntimeFailPrefix, cause)
	} else {
		degraded.Summary = "R3 判定失败（模型输出不可解析），已全部降级为需人工确认"
	}
	for _, ac := range spec.ACs {
		degraded.Checks = append(degraded.Checks, plugin.ACCheck{
			ID: ac.ID, Status: plugin.ACUnverifiable,
			Detail: detail,
		})
	}
	return degraded
}

// reactMaxIterations ReAct 迭代上限随清单规模派生（深挖修复：硬编码 12 对大清单
// 不够用、小任务浪费）：base 12，AC 超 10 条后每条 +1，封顶 24。
func reactMaxIterations(acCount int) int {
	n := 12
	if acCount > 10 {
		n += acCount - 10
	}
	if n > 24 {
		n = 24
	}
	return n
}

// runAgent 运行 R3 ReAct agent（goagent-runtime agentloop，迁移后样板
// 见 loopadapt 接缝）。
// toolsFactory 每次调用重建工具表：agentkit v0.7.0 起 Config.ToolsFactory 生效于
// 每次尝试——toolprior.WithCallLimit 的限流计数按尝试重置，重试不再继承首轮
// 已耗尽的外部调用预算（此前共享实例导致重试轮外部工具一开始就被软止损）。
func (w *Workflow) runAgent(ctx context.Context, instruction, query string, toolsFactory func() []tool.BaseTool, maxIterations int) (string, error) {
	return runAgent(w, ctx, instruction, query, toolsFactory, maxIterations)
}
