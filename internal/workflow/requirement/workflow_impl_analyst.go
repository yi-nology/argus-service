// workflow_impl_analyst.go —— Workflow 分析师综合段的包级实现。

package requirement

import (
	"context"

	"github.com/cloudwego/eino/schema"
	"github.com/yi-nology/argus/internal/plugin"
	"github.com/yi-nology/argus/internal/reporttmpl"
)

// synthesisAnalyst 的包级实现（转发壳见原文件）。
func synthesisAnalyst(w *Workflow, ctx context.Context, task plugin.Task,
	merged *plugin.MergedFindings, summary string) *plugin.AnalystSections {
	if task.Artifacts[plugin.ArtifactReportTemplate] != reporttmpl.Analyst {
		return nil
	}
	if w.LLM == nil {
		w.degradeAnalyst(merged, "LLM 未配置")
		return nil
	}
	diffEmpty := task.Diff == nil || len(task.Diff.Files) == 0
	if (merged == nil || len(merged.Findings) == 0) && diffEmpty {
		return nil // 无可综合材料，不发空调用
	}

	sys := reporttmpl.AnalystSynthesisPrompt(reporttmpl.Analyst)
	user := buildAnalystInput(task, merged, summary)
	resp, err := w.LLM.Generate(ctx, plugin.LayerR4X, []*schema.Message{
		schema.SystemMessage(sys), schema.UserMessage(user),
	})
	if err != nil {
		w.degradeAnalyst(merged, err.Error())
		return nil
	}
	wire, err := plugin.DecodeFindingsJSON(resp.Content)
	if err != nil {
		w.degradeAnalyst(merged, "输出解析失败: "+err.Error())
		return nil
	}
	sec := plugin.SanitizeAnalyst(&wire)
	// v6.17.0 方向 A 证据可信度：Claims 证据交叉校验 + 高风险状态降级。
	// 校验面 = 改动集（Diff.Files.Path）+ 触达集（ArtifactCodegraphTouched 渲染面
	// 提取的 file:line 引用）；无引用的纯文字差距说明不算证据缺陷；证据不可验证
	// 时 Evidence 追加 ⚠️ 注记（软标注）；高风险状态（missing/inconsistent）证据
	// 不可验证时降级 partial（状态错误比缺失更有害，但降级而非删除保留线索）。
	plugin.VerifyClaimsEvidence(&sec, changedPaths(task), touchedPaths(task))
	// 全空产出（模型按纪律对空洞输入返回零值段）= 未产出，降级注明。design
	// 的 triggered=false 是「判定未命中」语义——只有连同内容全空才算空产出。
	if len(sec.Claims) == 0 && len(sec.TestGaps) == 0 && len(sec.OpenQuestions) == 0 && designEmpty(sec.Design) {
		w.degradeAnalyst(merged, "产出为空")
		return nil
	}
	w.Log.Info("argus.analyst.done", "claims", len(sec.Claims),
		"design_triggered", sec.Design != nil && sec.Design.Triggered,
		"test_gaps", len(sec.TestGaps), "open_questions", len(sec.OpenQuestions))
	return &sec
}

// degradeAnalyst 的包级实现（转发壳见原文件）。
func degradeAnalyst(w *Workflow, merged *plugin.MergedFindings, reason string) {
	w.Log.Warn("argus.analyst.degraded", "reason", reason)
	if merged != nil {
		merged.Degraded = append(merged.Degraded, "分析师综合段未产出（"+reason+"），本报告不含声明对照/结构维度段落")
	}
}
