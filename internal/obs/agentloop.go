// agentloop.go —— ReAct 运行遥测指标（v8.6）。
//
// label=builtin|qa|r3。runs 按终态分桶；attempts 与 rounds 的总量反映回喂
// 重试与回合消耗；uncertain 非零即 fail-closed（正常应恒为 0，出现即需排查）。
// 从 obs.go 拆出：主文件保持在 arch 的 400 行阈值内。

package obs

import "github.com/prometheus/client_golang/prometheus"

var (
	agentLoopRuns = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "argus_agentloop_runs_total", Help: "ReAct 运行次数（label × 终态）",
	}, []string{"label", "status", "tenant"})
	agentLoopAttempts = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "argus_agentloop_attempts_total", Help: "ReAct attempt 次数（含回喂重试）",
	}, []string{"label", "tenant"})
	agentLoopRounds = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "argus_agentloop_rounds_total", Help: "ReAct 回合数（模型调用轮次，含重试累加）",
	}, []string{"label", "tenant"})
	agentLoopToolCalls = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "argus_agentloop_tool_calls_total", Help: "ReAct 工具调用数（含重试累加）",
	}, []string{"label", "tenant"})
	agentLoopUncertain = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "argus_agentloop_uncertain_total", Help: "ReAct 未知态工具调用数（非零=需对账）",
	}, []string{"label", "tenant"})

	// 单工具调用面（v8.6）：status=ok|error；耗时含工具执行体。
	toolCalls = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "argus_tool_calls_total", Help: "ReAct 工具调用次数（label × tool × 状态）",
	}, []string{"label", "tool", "status", "tenant"})
	toolDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "argus_tool_duration_seconds",
		Help:    "ReAct 工具执行耗时分布",
		Buckets: []float64{0.01, 0.05, 0.1, 0.5, 1, 2, 5, 10, 30, 60},
	}, []string{"label", "tool", "tenant"})
)
