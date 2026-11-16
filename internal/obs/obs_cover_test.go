// obs_cover_test.go —— 观测面补测（#36 kernel 60/70）：Init 注册 + plugin 钩子
// 接线、P2-05 导出计数族、SSE 连接 gauge、控制台白名单快照面。
// Init 只在本包测试进程调用一次（prometheus MustRegister 重复注册会 panic）。
package obs

import (
	"testing"
	"time"

	"github.com/yi-nology/argus/internal/plugin"
)

// TestInitAndCounterFamily Init 一次 + 全量 Incr 族过一遍（冒烟：标签词表合法、
// 不 panic、ConsoleMetrics 白名单快照可见）。
func TestInitAndCounterFamily(t *testing.T) {
	Init() // 幂等一次；重复调用交给生产（单例进程）

	// 导出计数族全过一遍（各出口的真实调用面）。
	IncrAuthzDenied("/tasks", "repo")
	IncrEventDropped("outbox_failed")
	IncrCommentRedelivery()
	IncrBudgetExceeded("quota")
	IncrBudgetExceeded("real")
	IncrSideEffectMismatch(2)
	IncrNotifyFailure("error")
	IncrWebhookSkipped("event_type")
	IncrWebhookRejected("signature")
	IncrWebhookEnsure("created")
	IncrLLMFailover("R3")
	IncrCtxFit("R1", "trunc")
	IncrLLMContinued("R2")
	IncrToolAnomaly("get_file")
	IncrR2SoftFailure()
	IncrQAFailure("llm")
	IncrPollerFailure("list")
	IncrCommentBusy()
	IncrTestGenVerify(true)
	IncrTestGenVerify(false)
	IncrFixVerify(true)
	IncrFixVerify(false)
	IncrFixPush("direct", "pushed")
	IncrCommentPostFailure("reply")

	// SSE gauge 加减对账。
	AddSSEConnections(3)
	AddSSEConnections(-1)

	// plugin 钩子接线（Init 装配面——nil 检查 + 调用不炸）。
	if plugin.HookTaskTotal == nil {
		t.Fatal("Init 应接通 plugin.HookTaskTotal")
	}
	reportSmoke()

	// LLM failover 计数面（原子累加）。
	before := LLMFailoverTotal()
	IncrLLMFailover("R35")
	if LLMFailoverTotal() != before+1 {
		t.Fatal("LLMFailoverTotal 应随 IncrLLMFailover 递增")
	}

	// 控制台白名单：判定面 + 快照面（白名单指标必有样本行；未注册指标缺席）。
	if !ConsoleMetricEnabled("argus_tasks_total") || ConsoleMetricEnabled("not_a_metric") {
		t.Fatal("ConsoleMetricEnabled 判定失真")
	}
	snap := ConsoleMetrics()
	if len(snap) == 0 {
		t.Fatal("白名单指标应有快照样本")
	}
	seen := map[string]bool{}
	for _, m := range snap {
		if !ConsoleMetricEnabled(m.Name) {
			t.Fatalf("快照漏白名单外指标: %s", m.Name)
		}
		seen[m.Name] = true
	}
	if !seen["argus_tasks_total"] {
		t.Fatalf("tasks_total 应在快照中（ReportSmoke 刚写过）: %v", seen)
	}
}

// reportSmoke 经 plugin 钩子写一轮全量指标（Init 接线验证；值不重要）。
func reportSmoke() {
	plugin.ReportTaskTotal("gitea", "code_review", "done")
	plugin.ReportStageDuration("R1", time.Second)
	plugin.ObsFindings("high", "sast", 2)
	plugin.ReportPluginFailure("sast")
	plugin.ReportLLMTokens("R1", "m", 100)
	plugin.ReportLLMCache("m", 10, 100)
	plugin.ReportFindingsByRepo("gitea", "acme/app", "high", 2)
	plugin.ReportTaskDurationByRepo("gitea", "acme/app", "code_review", 1.5)
	plugin.ReportReflectDrop("low_confidence")
	plugin.ReportReflectDegrade("evidence_gate")
	plugin.ReportLayerExec("poster")
	plugin.ReportVerdict("gitea", "code_review", "approve", "R5.1")
	plugin.ReportFenceNeutralized(3)
	plugin.ReportPluginSkipped("disabled")
	plugin.ReportCommentRoute("review", "keyword")
	plugin.ReportTaskDeduped("last_head_sha")
	plugin.ReportBestOfN("sast", "majority")
	SetTaskQueueDepth(2, 1)
	AddCtxBreakdown("R3", "system", 120)
}
