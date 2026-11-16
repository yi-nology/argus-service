// service.go —— 门面核心：Service 结构、日志/评论闸与统计/安装事件面
// （包文档见 doc.go；Q&A 引擎装配与追问面见 service_qa.go）。

package service

import (
	"context"
	"sync"
	"time"

	"github.com/cloudwego/eino/components/model"
	gitmodel "github.com/yi-nology/go-git-platform/model"

	"github.com/oagentkit/tools/workcopy"
	"github.com/oekit/concurrency/ratelimit"
	"github.com/oekit/concurrency/syncx"
	datacache "github.com/oekit/data/cache"
	"github.com/oekit/pkg/container"

	oagentprogress "github.com/oagentkit/runtime/progress"
	argus "github.com/yi-nology/argus/biz/model/argus"
	"github.com/yi-nology/argus/internal/argusctx"
	"github.com/yi-nology/argus/internal/config"
	"github.com/yi-nology/argus/internal/dispatch"
	"github.com/yi-nology/argus/internal/memorykernel"
	"github.com/yi-nology/argus/internal/progress"
	"github.com/yi-nology/argus/internal/qa"
	"github.com/yi-nology/argus/internal/semsearch"
	"github.com/yi-nology/argus/internal/storage"

	"github.com/oagentkit/llm"
	"github.com/oagentkit/obs/audit"
	"github.com/oagentkit/rag"
)

// Argus-specific audit actions (oagentkit/obs/audit provides the Logger, actions are domain-specific).
const (
	auditTaskCreate      audit.Action = "task.create"
	auditFindingSuppress audit.Action = "finding.suppress"
	// v3.86.0 补面：verdict 终态/误抑制恢复/修复命令（fix_requested 与 fix_done
	// 在 service，verdict.posted 与 fix.push 在 runner——同一 auditLog 单源）。
	auditVerdictPosted  audit.Action = "verdict.posted"
	auditFixPush        audit.Action = "fix.push"
	auditFindingRestore audit.Action = "finding.restore"
	auditFindingFixReq  audit.Action = "finding.fix_requested"
	auditFindingFixDone audit.Action = "finding.fix_done"
)

// Service 门面：biz handler 只跟它说话。
type Service struct {
	Cfg       *config.Config
	Audit     *audit.Logger // 审计日志（nil 安全：Logger.Log 对 nil receiver no-op，调用处免判空）
	Store     *storage.Store
	SDK       *argusctx.SDKCtx
	Registry  *dispatch.Registry
	Log       Logger
	Version   string
	Bus       *oagentprogress.Bus[progress.Event] // 任务进度事件（v3.1 SSE 数据源；nil 安全）
	EventSeen *progress.Dedup                     // 实例级事件去重环（v6.12 P1：本地发布登记 + fanout 回灌查环，同实例 at-most-once；nil = 旧语义）
	Knowledge rag.KnowledgeService                // 知识库（误报反馈回流；nil/Noop = 只抑制不回流）
	QAModel   model.BaseChatModel                 // Q&A 专用模型（P6 覆盖；nil = 用 Model）
	Model     model.BaseChatModel                 // LLM 模型（Q&A 直接调用；nil = Q&A 禁用）
	// LLM 生成器（v3.123.0，models.Generate 同源接线）：diff 预审等单轮写作
	// 委派 writing.Service 消费；nil = ReviewDiff/CreateDiffTask 禁用。
	LLM llm.Generator
	// RouterModel 评论意图路由分类模型（v3.4 ARGUS_ROUTER_MODEL 覆盖；nil = QAModel → Model）。
	// 仅 ARGUS_COMMENT_ROUTER=1 时消费。
	RouterModel model.BaseChatModel

	// WC 工作副本池（v3.22.0 testgen 沙箱验证；nil = 验证不可用）。
	WC *workcopy.Pool

	// SemSearch 工作副本语义检索底座（v3.126.0，bootstrap 按 ARGUS_SEMSEARCH +
	// ARGUS_EMBEDDING_* 双门构建；nil = search_sem 运行期报未启用）。
	SemSearch *semsearch.Manager

	// MemKernel 评审记忆内核（v3.151.0，ARGUS_MEMORY_KERNEL opt-in；
	// nil = 文件面 LoadIndex 注入）。
	MemKernel *memorykernel.Kernel

	// qaEngine 问答引擎单例（Engine 无自有状态、字段全共享引用，会话在 Store）。
	// 此前每次 Ask 都重建，纯浪费；sync.Once 保证并发首建安全。
	qaOnce sync.Once
	qaEng  *qa.Engine

	// 评论链路资源闸（v3.8.0）：有界并发 + 超时 + 停机排水。此前评论事件挂
	// 无超时的 Background ctx、无并发上限、停机直接杀在途 ReAct（用户永无回复）。
	// v3.178.0 手写 sem chan 收编 oekit/concurrency/syncx.Gate.TryAcquire
	//（v0.20.0 补的非阻塞档——正是本闸的 fail-fast 忙拒收语义）。
	commentOnce sync.Once
	commentGate *syncx.Gate    // 有界并发（容量 Cfg.CommentConcurrency）
	commentWG   sync.WaitGroup // 在途评论事件（DrainComments 等待）

	// qaThrottle Q&A 评论 per-PR 30s 冷却缓存（v3.84.0 回归 per-Service 实例：
	// v3.82.0 曾改进程级单例，e2e 同进程多 Service 场景互相污染节流窗口）。
	// 懒建（qaThrottleMu 护栏），生产行为不变——进程内恒一个 Service。
	qaThrottleMu  sync.Mutex
	qaThrottleCch *datacache.LocalCache[bool]
	// collabCch 仓库协作者名单缓存（v5.3.0 评论指令门控，TTL 5 分钟，qaThrottleMu 护栏）。
	collabCch *datacache.LocalCache[string]

	// rateLimiter 每 token 速率限制器（v3.135.0 引入；v3.147.0 换 oekit
	// ratelimit.KeyedLimiter：按 key 隔离令牌桶 + TTL 驱逐闲置 key；tokenRateOK
	// 懒建，key=token 指纹）。
	rateOnce    sync.Once
	rateLimiter *ratelimit.KeyedLimiter

	// fixInflight @argus fix 在途去重（v3.85.0）：key=pr+fp，防同 finding 并发
	// 双跑烧双倍 agent token。进程级即可（修复是交互式命令，不跨重启）。
	// 用上游 oekit/pkg/container.Map 取代裸 sync.Map：键值类型化，取用免断言。
	// 取值字段（零值即可用，Service 构造点无须逐个初始化）。
	fixInflight container.Map[string, bool]
}

// Logger service 层的最小日志接口（logx.Logger 满足）。
type Logger interface {
	Info(msg string, keysAndValues ...any)
	Warn(msg string, keysAndValues ...any)
	Error(msg string, keysAndValues ...any)
}

// log 返回安全 Logger（nil → logx.Nop；v5.3.2：上游 logx v0.8.0 提供 Nop，
// 删本地 noopLogger 同构）。
func (s *Service) log() Logger { return log(s) }

// initCommentGate 惰性初始化评论并发闸（容量来自 Cfg.CommentConcurrency）。
func (s *Service) initCommentGate() { initCommentGate(s) }

// SubmitComment 评论事件入口（v3.8.0 治理）：有界并发 + 整体超时 + 停机排水。
// ctx 治理：WithoutCancel 保留请求 ctx 的 trace_id（此前 Background 直接丢弃），
// 同时脱钩请求生命周期（平台 10s 断连不再杀在途 ReAct）；再包 CommentTimeout
// 整体上界（此前理论可达 30 分钟级、无任何 deadline）。
// 返回 false = 并发闸满载（调用方回"忙"，不排队——排队会把 webhook 线程池拖垮）。
func (s *Service) SubmitComment(requestCtx context.Context, ev *gitmodel.NormalizedEvent, delivery string) bool {
	return submitComment(s, requestCtx, ev, delivery)
}

// DrainComments 停机排水：等待在途评论事件收尾（最多 timeout，超时放行——
// 与 worker 两阶段停机同一哲学，避免卡死整个 shutdown）。
func (s *Service) DrainComments(timeout time.Duration) { drainComments(s, timeout) }

// ---------- 统一响应壳（oekit/middleware mw：{code,data,message,trace_id}）----------
// trace_id 由 oekit/middleware/hmw 注入（main 注册），mw 响应函数自动携带。

// requirePlatform 平台准入检查：规范化 platform 名并返回 (pcfg, true) 表示已接入且有凭证（PAT 或 GitHub App）。
func (s *Service) requirePlatform(platform string) (string, *config.PlatformConfig, bool) {
	return requirePlatform(s, platform)
}

// handleInstallationEvent 处理 GitHub App installation / installation_repositories 事件。
// 记录安装信息到日志（安装 ID、账号、仓库列表），不建审查任务。
// SDK 的 ParseWebhookEvent 对 installation 事件只填充 Type + RawPayload，
// 需要从原始 payload 提取结构化信息。
func (s *Service) handleInstallationEvent(ev *gitmodel.NormalizedEvent, delivery string) *argus.WebhookResponse {
	return handleInstallationEvent(s, ev, delivery)
}

// recordFalsePositiveKnowledge 误报结论回流知识库（Milvus 模式；Local 模式跳过——
// configmap 只读目录不可写，且跨仓库泛化本就需要向量语义）。内容经 safejson
// 转义（半可信：包含开发者手输的反馈语境）。幂等：同 fp 重复反馈重复入库无碍
// （检索打分摊薄），主键确定性 file#chunk_N 覆盖旧行。
func (s *Service) recordFalsePositiveKnowledge(ctx context.Context, platform, owner, repo, number, fp, file, comment string) {
	recordFalsePositiveKnowledge(s, ctx, platform, owner, repo, number, fp, file, comment)
}

// ---------- MCP 副驾数据面（v3.15.0 P2-7；channel/mcpserver.Copilot 实现）----------

// ReviewDiff 本地 diff 审查（v3.74.1，argus-copilot pre-commit 路径）：
// 单轮 LLM 审查任意 diff 文本，返回 markdown 意见列表。不建任务、不落库、
// 不回帖——本地快审语义；模型未配置返回错误（调用方显式降级）。
// diff 上限 120KB（与写作面同量级；超限截断由调用方负责，工具入参侧也拦）。
// v3.123.0 起委派 writing.Service.ReviewDiff（与 diff_preview 异步任务同一
// 执行体，prompt 单源）——本层只留 nil 守卫与语义注释。
func (s *Service) ReviewDiff(ctx context.Context, diff string) (string, error) {
	return reviewDiff(s, ctx, diff)
}

// StatsOverview 审查概览聚合（GET /stats/overview，🔒 read 档，v3.88.0）：
// 按仓任务量 / verdict 分布 / token 用量 / 抑制计数。days 窗口缺省 30，
// 钳制 1..365；platform/owner/repo 过滤可选。仪表盘（review-service 前端）
// 的数据面——argus 自身不做 UI（v3.84 轮裁定）。
// v6.7.0 P0-02：token 带仓库约束时约束外仓库的行不透出（跨仓任务量/verdict
// 元数据此前全量聚合）。
func (s *Service) StatsOverview(ctx context.Context, platform, owner, repo string, days int) (map[string]any, int) {
	return statsOverview(s, ctx, platform, owner, repo, days)
}
