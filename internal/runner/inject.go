// inject.go —— 任务前置注入器（OCR delegate / SAST / 全文检索）与 diff/worktree
// 辅助。与 runner 主编排（Execute/run）分离：注入器只读工作副本并往
// Task.Artifacts / Workflow 写产物，失败一律告警降级、不阻断审查
// （代码图/影响面见 inject_codegraph.go，跨仓契约见 inject_related.go）。

package runner

import (
	"context"
	"strings"

	"github.com/oagentkit/tools/workcopy"

	"github.com/yi-nology/argus/internal/plugin"
	"github.com/yi-nology/argus/internal/workflow/requirement"
)

// reviewGuidelinesPath 仓库级审查指引文件（默认分支根目录）。
const reviewGuidelinesPath = ".argus/review-guidelines.md"

// reviewGuidelinesMaxRunes 指引注入 prompt 的长度上限。
const reviewGuidelinesMaxRunes = 4000

// fetchReviewGuidelines 读取仓库默认分支的审查指引。任何失败（无文件/无权限/
// 网络错误）都视为"无指引"：审查照常进行，不因指引缺失而失败。
func (r *Runner) fetchReviewGuidelines(ctx context.Context, pr *plugin.PRContext) string {
	return fetchReviewGuidelines(r, ctx, pr)
}

// injectOCRDelegate OCR delegate 委托前置（design §2）：dispatch 前在工作副本执行
// `ocr delegate preview/rule`（确定性子命令，OCR 端零 LLM、零凭证），把文件分诊与
// 仓库规则组渲染成提示词 section 注入 Artifacts。仅 code_review 任务生效；未配置
// ARGUS_OCR_BIN、无工作副本池或任何执行失败 → 告警降级，审查照常。
func (r *Runner) injectOCRDelegate(ctx context.Context, pr *plugin.PRContext,
	diff *plugin.DiffContext, ptask *plugin.Task, knobs taskKnobs) {
	if !knobs.ocrDelegateEnabled {
		repo := ""
		if pr != nil {
			repo = pr.Owner + "/" + pr.Repo
		}
		r.Log.Info("argus.runner.ocr_delegate_repo_off", "repo", repo,
			"reason", "repo_settings.ocr_delegate_enabled=false（按仓关闭）")
		return
	}
	injectOCRDelegate(r, ctx, pr, diff, ptask)
}

// injectOCRDelegateInDir delegate 前置的执行段（dir 由调用方就绪；pool 语义在
// injectOCRDelegate，本函数可独立单测）。
func (r *Runner) injectOCRDelegateInDir(ctx context.Context, dir string, pr *plugin.PRContext,
	paths []string, ptask *plugin.Task) {
	injectOCRDelegateInDir(r, ctx, dir, pr, paths, ptask)
}

// injectSAST 确定性 SAST 前置（v3.15.0 P0-3）：工作副本上跑 gitleaks/semgrep，
// 产出作为独立 Outcome 注入 workflow（finish 内并入合并，零 LLM、零任务 token）。
// 未配置 ARGUS_SAST_* 或无工作副本池时静默跳过；扫描失败由 workflow 降级注明。
func (r *Runner) injectSAST(ctx context.Context, pr *plugin.PRContext,
	diff *plugin.DiffContext, wf *requirement.Workflow) {
	injectSAST(r, ctx, pr, diff, wf)
}

// injectCodeSearch 全文检索前置（v3.84.0）：就绪工作副本路径注入 Workflow，
// 供 search_code 工具按需读取。注入只记路径不持引用——目录由 workcopy 池
// TTL 兜底存活（任务时长远短于 TTL），读不到时工具侧按错误文本降级。
// 未启用/无工作副本池/无纳入文件 → 跳过；失败告警降级，不阻断审查。
func (r *Runner) injectCodeSearch(ctx context.Context, pr *plugin.PRContext,
	diff *plugin.DiffContext, wf *requirement.Workflow) {
	injectCodeSearch(r, ctx, pr, diff, wf)
}

// hasIncludedGoFiles 变更中是否有进入审查文本的 Go 文件。
func hasIncludedGoFiles(diff *plugin.DiffContext) bool {
	if diff == nil {
		return false
	}
	for _, f := range diff.Files {
		if f.Included && strings.HasSuffix(f.Path, ".go") {
			return true
		}
	}
	return false
}

// includedPaths diffprep 收进 Text 的文件（OCR delegate rule 的路径入参与
// argus 实际审查范围保持一致）。
func includedPaths(diff *plugin.DiffContext) []string {
	if diff == nil {
		return nil
	}
	paths := make([]string, 0, len(diff.Files))
	for _, f := range diff.Files {
		if f.Included {
			paths = append(paths, f.Path)
		}
	}
	return paths
}

// worktreeKey 将 PRContext 转为 workcopy.WorktreeKey（单源 plugin.PRToKey）。
func worktreeKey(pr *plugin.PRContext) workcopy.WorktreeKey {
	return plugin.PRToKey(*pr)
}
