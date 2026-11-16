package requirement

import (
	"context"
	"strings"

	"github.com/oagentkit/textx/textutil"

	"github.com/yi-nology/argus/internal/plugin"
)

// 缺省需求文本截断上限（字符）——Workflow 未装配 config 派生值时的兜底。
const (
	defaultReqBodyMaxChars  = 24_000
	defaultReqIssueMaxChars = 8_000
)

// SummarizeChange R2：变更理解 → ChangeSummary（R3/R4 复用的 diff 公共前缀，省 token）。
func (w *Workflow) SummarizeChange(ctx context.Context, task plugin.Task) (string, error) {
	return summarizeChange(w, ctx, task)
}

// r2MinValidRunes R2 摘要最小有效长度——少于此值视为模型输出失效（空转/拒答）。
const r2MinValidRunes = 20

// onStage 阶段进度上报（nil 安全；runner 接线 SSE 发布）+ 进度标记落库
// （v3.132.0）：每阶段开头持久化 stage_progress，get_report 轮询据此显示
// 「当前在跑哪一步」——此前只有 checkpoint/debug 产物可推导，RunReviewOnly
// 跳过 R1/R3/R35 时被推导成"没做到"，轮询恒显示 R1。
func (w *Workflow) onStage(stage string) {
	onStage(w, stage)
}

const (
	// detailMaxBytes 单条详情硬上限（防异常工具输出打爆磁盘与取回面）。
	detailMaxBytes = 200_000
	// detailOverflowMax codegraph 类工具进上下文的截断正文上限。
	detailOverflowMax = 4_000
)

// v6.15.0：spillRefRe 白名单正则退役——取回面收编 goagent-kernel/spill.ReadPage
//（pathx.IsSafeFileName 净化 + 会话目录守卫 + 分页语义全在上游单源，比本仓
// 手写正则更严：Windows 盘符相对路径同拒）。

// stashDetail 超限全文落盘存档（v5.0.0 收编 harness spill 语义（今 goagent-kernel）：磁盘面取代
// v3.39.0 进程内 map——checkpoint 复活/进程重启后 get_detail 不再落空；会话隔离 =
// 任务 ID 目录，职责分工与 spill 一致：存档/取回判定单源，文案与工具壳归宿主）。
// 写失败返回空串（stashOverflowAt 退纯截断，不阻塞工具输出主链路）。
func (w *Workflow) stashDetail(content string) string {
	return stashDetail(w, content)
}

// stashOverflowAt 超限两级化：全文入任务级详情存档，进上下文的只留 max 内正文 +
// 取回指引。未超限原样返回（零开销直通）；存档写失败退纯截断（如实告知全文丢弃，
// 不留取得到不了的指引）。
func (w *Workflow) stashOverflowAt(full string, max int) string {
	return stashOverflowAt(w, full, max)
}

// ---------- 小工具 ----------

func truncSummary(s string, n int) string {
	return textutil.TruncEllipsis(strings.TrimSpace(s), n)
}
