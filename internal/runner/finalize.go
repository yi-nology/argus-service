// finalize.go —— 终态收口漏斗：cause→status 选择、fenced 分流、公告与指标单源。
//
// runner 的 7 个终态路径（lifecycle ×2、execute ×1、writing ×3、usage ×1）统一
// 经 finishTask 落库；调用点旁路副作用（debug 产物、start marker、sink、findings
// 指标等）内容不变：需要保持「写库 → 旁路 → 公告」原交错的站点经 AfterWriteLive
// 注入（仅未 fenced 时执行）；fenced 时也要运行的失败 marker 经 AfterWrite
// （fenced 判定前）注入。

package runner

import (
	"context"

	"github.com/yi-nology/argus/internal/plugin"
	"github.com/yi-nology/argus/internal/storage"
)

// terminalCause 调用方声明的终态原因；finishTask 据此选任务 status。
type terminalCause int

const (
	causeDone terminalCause = iota
	causeStale
	causeFailed
)

// terminalStatus cause→任务终态（done/stale/failed 单源；未知 cause 按 failed
// 兜底——终态路径宁可显式失败，不可静默停在 running）。
func terminalStatus(c terminalCause) string {
	switch c {
	case causeDone:
		return storage.StatusDone
	case causeStale:
		return storage.StatusStale
	default:
		return storage.StatusFailed
	}
}

// terminalWrite 一次终态写入的全部载荷（字段与 FinishTaskFenced 参数一一对应）。
// Out 只供公告取 verdict/rule/tokens/findings，不参与落库。
// AfterWrite 是写库成功后、fenced 判定前的旁路钩子：fenced 短路前执行，因此即便
// 本次写入被归属 fencing 拒绝，依赖写入成功的旁路（站点 3/7 的失败 marker）仍按原样
// 运行。AfterWriteLive 是 fenced 判定后、仅未 fenced 时执行、公告前的旁路钩子
// （站点 2/4/5/6 的 debug/sink/start marker——base 在 fenced 早退后才执行）。
type terminalWrite struct {
	Task             *storage.Task
	Cause            terminalCause
	Verdict          string
	Report           string
	ResultJSON       string
	FailReason       string
	Tokens           int
	SummaryCommentID int64
	Out              *plugin.WorkflowOutput
	Announce         bool
	AfterWrite       func()
	AfterWriteLive   func()
}

// finishTask 终态漏斗：终态选择 → 归属 fencing 写入 → 写后旁路 → 公告 → 指标。
//
// 行为：
//  1. 按 Cause 选 status 后调 FinishTaskFenced（参数顺序与原 7 个调用点逐字一致）；
//  2. 写库成功后先执行 AfterWrite（fenced 判定前的旁路），再判 fenced；
//  3. 写库错误原样返回，调用方按各自原失败路径接手（日志/错误包装文案不变）；
//  4. fenced=true（归属已易主）时只记统一的 finish_fenced，不公告、不报指标，
//     返回 (true, nil)——僵尸执行体不得替接管方发终态事件；
//  5. 未 fenced 时执行 AfterWriteLive（fenced-gated 的写后旁路），再公告
//     （事件/audit/notify）并记任务终态指标。公告 reason 仅 failed 终态携带，
//     done/stale 传空串（与原调用点逐站一致）。
//
// 终态写与公告都用 context.WithoutCancel（kernel finalize.Detached 语义）：
// 请求 ctx 已取消时终态仍要落库，通知也不应因取消而丢失。
func finishTask(r *Runner, ctx context.Context, w terminalWrite) (fenced bool, err error) {
	status := terminalStatus(w.Cause)
	fenced, err = r.Store.FinishTaskFenced(context.WithoutCancel(ctx), w.Task.ID, status, w.Verdict, w.Report,
		w.ResultJSON, w.FailReason, w.Tokens, w.SummaryCommentID)
	if err != nil {
		return false, err
	}
	if w.AfterWrite != nil {
		w.AfterWrite()
	}
	if fenced {
		// 归属已易主（stale 重置）：本次终态写入被条件更新拒绝——公告与指标
		// 一并跳过，否则会重复接管方已发出的终态。
		r.Log.Warn("argus.runner.finish_fenced", "task", w.Task.ID,
			"hint", "任务归属已易主（stale 重置），本次终态写入丢弃")
		return true, nil
	}
	if w.AfterWriteLive != nil {
		w.AfterWriteLive()
	}
	if w.Announce {
		reason := ""
		if w.Cause == causeFailed {
			reason = w.FailReason
		}
		r.announceTerminal(context.WithoutCancel(ctx), w.Task, status, w.Verdict, w.Out, reason)
		plugin.ReportTaskTotal(ctx, w.Task.Platform, w.Task.Kind, status)
	}
	return false, nil
}
