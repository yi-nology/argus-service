// poll.go —— review -async 的提交方轮询（自 main.go 拆出，行为不变）。
package copilot

import (
	"github.com/yi-nology/argus/internal/storage"
	"context"
	"encoding/json"
	"fmt"
	"time"

	mcpclient "github.com/mark3labs/mcp-go/client"
)

// pollOpts poll 的机器可读面选项（与 review 同契约）。
type pollOpts struct {
	JSONOut bool
	FailOn  string
}

// pollTask 轮询 get_report 至终态（v3.123.0 diff_preview 闭环的提交方侧）：
// done/stale → 打印报告并按意见阈值给退出码；failed/超时 → 报错（退出码 2）。
// 10min 上限、3s 间隔。
func pollTask(ctx context.Context, cli *mcpclient.Client, taskID string, opts pollOpts) (string, int, error) {
	deadline := time.Now().Add(10 * time.Minute)
	lastStage := "" // 阶段进度只在变化时打 stderr，等待期不静默（v3.127.0）
	for {
		out, err := call(ctx, cli, "get_report", map[string]any{"task_id": taskID})
		if err != nil {
			return "", 0, err
		}
		var view map[string]any
		if jerr := json.Unmarshal([]byte(out), &view); jerr != nil {
			return out, exitClean, nil // 非 JSON 视为错误文本，原样透传
		}
		status, _ := view["status"].(string)
		if cur, _ := view["stage_current"].(string); cur != "" && cur != lastStage {
			lastStage = cur
			warnf("任务 %s 进行中，当前阶段 %s", taskID, cur)
		}
		switch status {
		case storage.StatusDone, storage.StatusStale:
			report, _ := view["report"].(string)
			findings := parseFindings(report)
			if !opts.JSONOut {
				if report != "" {
					return report, thresholdExit(findings, opts.FailOn), nil
				}
				return out, exitClean, nil
			}
			res := reviewResult{
				Mode: "poll", Clean: len(findings) == 0, Findings: findings,
				Report: report, TaskID: taskID, Status: status,
			}
			if v, ok := view["verdict"].(string); ok {
				res.Verdict = v
			}
			if st, ok := view["stages"]; ok {
				res.Stages = st
			}
			if status == storage.StatusStale {
				res.Note = "审查完成但已有更新的提交，报告未回帖（结论仅对旧 head 成立）"
			}
			b, _ := json.MarshalIndent(res, "", "  ")
			return string(b), thresholdExit(findings, opts.FailOn), nil
		case storage.StatusFailed:
			reason, _ := view["fail_reason"].(string)
			return "", 0, fmt.Errorf("预审失败: %s", reason)
		}
		if time.Now().After(deadline) {
			return "", 0, fmt.Errorf("轮询超时（10min），任务 %s 仍在执行，稍后重跑 poll", taskID)
		}
		select {
		case <-ctx.Done():
			return "", 0, ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}
}
