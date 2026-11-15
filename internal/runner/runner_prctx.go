// runner_prctx.go —— PR 上下文构建面：PRContext/diff 增量/仓库配置与审查指引（RepoGuide）。

package runner

import (
	"context"

	datacache "github.com/oekit/data/cache"

	"github.com/yi-nology/argus/internal/argusctx"
	"github.com/yi-nology/argus/internal/plugin"
	"github.com/yi-nology/argus/internal/storage"
)

// buildPRContext 从平台 API 组装 PR 上下文（CLI 任务此刻才拿到 head SHA）。
func (r *Runner) buildPRContext(ctx context.Context, task *storage.Task) (*plugin.PRContext, error) {
	return buildPRContext(r, ctx, task)
}

// fetchCIStatus 取 head SHA 的合并 CI 状态（v6.11.0 流水线进报告 meta 的数据源）。
// best-effort 三降级：平台无 CommitStatusManager 能力 / 调用失败 / 无状态——
// 一律静默返回空串（渲染层省略该段），绝不阻断审查。统一词表由 SDK 后端归一
// （CombineCommitStatusStates 取最坏状态：任一 context 失败即失败）。
func (r *Runner) fetchCIStatus(ctx context.Context, platform, owner, repo, sha string) string {
	return fetchCIStatus(r, ctx, platform, owner, repo, sha)
}

// buildDiffIncremental 拉取 diff 并尝试增量清洗（diff-of-diffs）。
// 仅在 synchronize 事件（PR 更新）且存在前置审查记录时激活增量路径；
// 其余场景退化为全量 Prepare。opts 为仓库级路径过滤（v3.15.0 review 段）。
// totalLimit 由调用方传按仓旋钮生效值（v3.187.0 taskKnobs.maxDiffChars）。
func (r *Runner) buildDiffIncremental(ctx context.Context, pr *plugin.PRContext, task *storage.Task,
	totalLimit int, opts ...argusctx.PrepareOptions) (*plugin.DiffContext, error) {
	return buildDiffIncremental(r, ctx, pr, task, totalLimit, opts...)
}

// loadRepoConfig 仓库主权配置（runner 侧单次加载，diff 过滤与 workflow 共用；
// v3.15.0 前该加载只在 workflow.loadRepoConfig，为传 diffprep 选项在此提前）。
// 失败按无配置 + 告警（与 workflow 侧同纪律）。
func (r *Runner) loadRepoConfig(ctx context.Context, pr *plugin.PRContext) *plugin.RepoConfig {
	return loadRepoConfig(r, ctx, pr)
}

// repoGuideMaxBytes 仓库评审规范单文件上限（ZCode AGENTS.md 同款 100KB）。
const repoGuideMaxBytes = 100_000

// repoGuideAgentMaxBytes 业界标准 agent 规范文件的单文件上限（AGENTS.md 等多为
// 项目级通用指引而非审查专属，注入预算给小一些，防多源叠加挤占 R3/R4 窗口）。
const repoGuideAgentMaxBytes = 32_000

// repoGuideAgentFiles 业界标准 agent 规范文件（2026 事实标准，兼容读第一个存在的）：
// AGENTS.md（跨工具通用）/ CLAUDE.md（Claude Code）/ .cursorrules（Cursor）。
// 全部命中时仍只取第一个——同一仓库多份 agent 规范的内容通常高度重叠，
// 全拼只会稀释注入预算。
var repoGuideAgentFiles = []string{"AGENTS.md", "CLAUDE.md", ".cursorrules"}

// repoGuideCache 评审规范缓存（repo@sha → 内容；同 sha 多任务/重试复用，
// 负缓存空串防同 sha 反复打平台 API）。v3.82.0 引入 oekit/data/cache：
// MaxSize LRU 精确淘汰（键含 sha 天然有限），取代手写 mutex+map+
// 「满 64 整体清空」换防——热条目不再被无差别逐出。
var repoGuideCache = datacache.NewLocalCache(datacache.LocalConfig[string]{MaxSize: 64})

// RepoGuideFor 读取被评审仓库的评审规范 @ head（v3.38.0，v3.71.0 多源）：
// .argus/REVIEW.md（argus 专属）+ 业界标准 agent 规范（AGENTS.md/CLAUDE.md/
// .cursorrules，第一个存在的）。两源独立截断后拼接，REVIEW.md 在前。
// 经平台 API 取 blob——tree 作用域天然无本地 symlink 逃逸面；头截断 + 提醒
// 标签转义（半可信输入）；缺失/失败返回空段（常态，debug 留痕）。
func (r *Runner) RepoGuideFor(ctx context.Context, platform, owner, repo, headSHA string) string {
	return repoGuideFor(r, ctx, platform, owner, repo, headSHA)
}

// repoGuideFile 单文件拉取 + 截断 + 提醒标签转义；缺失/失败返回空串。
func (r *Runner) repoGuideFile(ctx context.Context, platform, owner, repo, headSHA, path string, maxBytes int) string {
	return repoGuideFile(r, ctx, platform, owner, repo, headSHA, path, maxBytes)
}
