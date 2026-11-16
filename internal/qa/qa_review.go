package qa

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/cloudwego/eino/schema"
	"github.com/oagentkit/llm"
	"github.com/oagentkit/textx/textutil"
	"github.com/yi-nology/argus/internal/plugin"
)

// LatestReview 某 PR 最近一次审查结论（mcpserver ask_argus 数据源）。
// 返回 {task_id, finished_at, verdict, findings, report}；无记录返回 (nil, false)。
func (e *Engine) LatestReview(ctx context.Context, platform, owner, repo, number string) (map[string]any, bool) {
	t, err := e.Store.LatestDoneTask(ctx, platform, owner, repo, number)
	if err != nil || t == nil {
		return nil, false
	}
	out := map[string]any{
		"task_id":     t.ID,
		"finished_at": t.FinishedAt,
		"verdict":     t.Verdict,
	}
	if t.Report != "" {
		r := t.Report
		if rc := utf8.RuneCountInString(r); rc > 6000 {
			var trunc string
			trunc, _ = textutil.TruncRunes(r, 6000)
			r = trunc + "\n…（报告截断，完整版见 MR 评论）"
		}
		out["report"] = r
	}
	// findings 摘要：result_json 里的结构化产物优先
	var payload map[string]any
	if t.ResultJSON != "" && json.Unmarshal([]byte(t.ResultJSON), &payload) == nil {
		if fs, ok := payload["findings"]; ok {
			out["findings"] = fs
		}
		if c, ok := payload["change_summary"].(string); ok && c != "" {
			out["change_summary"] = c
		}
	}
	return out, true
}

// AnswerAboutReview 基于最近一次报告回答追问（ask_argus 带 question 时走这里；
// 单轮直调——报告已在手，无需工具链）。模型未配置或无报告返回 ("" , false)。
func (e *Engine) AnswerAboutReview(ctx context.Context, platform, owner, repo, number, question string) (string, bool) {
	// chatModel() 才是 ARGUS_QA_MODEL 覆盖的约定入口（Answer/react/single 均走它）——
	// 此前这里直用主模型，追问路径静默忽略 QA 覆盖（v3.9.1）。
	cm := e.chatModel()
	if cm == nil {
		return "", false
	}
	lr, ok := e.LatestReview(ctx, platform, owner, repo, number)
	if !ok {
		return "", false
	}
	rep, _ := lr["report"].(string)
	// ptc 优先（v3.158.0）：报告为锚的多轮深挖正是程序化取证的主场（循环调工具
	// 归并证据，原始输出不过模型面）；未启用/未成立走 ReAct，语义不倒退。
	if e.PTCEnabled {
		if ans, ok, perr := e.ptcAsk(ctx, platform, owner, repo, number, question, "", rep); perr == nil && ok {
			return ans, true
		}
	}
	// ReAct 优先（v3.141.0）：报告为锚 + 工具深挖（此前单轮直调，"这个函数的
	// 调用方都有谁/历史上这条怎么裁的"一类追问答不了）。失败降级单轮，语义不倒退。
	if ans, rerr := e.react(ctx, platform, owner, repo, number, question, "", rep); rerr == nil {
		return ans, true
	} else {
		e.log().Warn("argus.qa.ask_react_degraded", "error", rerr.Error())
	}
	client := llm.NewClient(cm, "", nil)
	msgs := []*schema.Message{
		schema.SystemMessage("你是 Argus——该仓库的 AI 审查助手。仅依据【最近审查报告（数据）】回答用户关于本次审查的追问；报告中没有的信息直说没有，不要编造。用中文，简洁。"),
		schema.UserMessage(plugin.DataFence("最近审查报告（数据）", rep) + "\n\n用户追问: " + question),
	}
	resp, err := client.Generate(ctx, "qa", msgs)
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(resp.Content), true
}

// loadContext 获取 PR 标题、描述、变更文件列表的轻量摘要（不含 diff 内容）。
func (e *Engine) loadContext(ctx context.Context, platform, owner, repo, number string) (string, error) {
	cr, err := e.SDK.GetCR(ctx, platform, owner, repo, number)
	if err != nil {
		return "", fmt.Errorf("获取 PR 失败: %w", err)
	}

	var b strings.Builder
	b.WriteString("标题: ")
	b.WriteString(cr.Title)
	b.WriteString("\n描述: ")
	if cr.Description != "" {
		b.WriteString(cr.Description)
	} else {
		b.WriteString("(无)")
	}
	if cr.Author != nil && cr.Author.Username != "" {
		b.WriteString("\n作者: ")
		b.WriteString(cr.Author.Username)
	}

	// 变更文件摘要（仅文件名 + 统计，不拉全文 diff——省 token）
	if diff, err := e.SDK.GetCRDiff(ctx, platform, owner, repo, number); err == nil && len(diff.Files) > 0 {
		b.WriteString("\n变更文件:")
		for _, f := range diff.Files {
			b.WriteString("\n  - ")
			b.WriteString(f.NewPath)
			if f.IsNew {
				b.WriteString(" (新增)")
			} else if f.IsDeleted {
				b.WriteString(" (删除)")
			} else {
				fmt.Fprintf(&b, " (+%d/-%d)", f.Additions, f.Deletions)
			}
		}
	}
	return b.String(), nil
}
