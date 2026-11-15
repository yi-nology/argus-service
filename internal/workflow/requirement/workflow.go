// workflow.go —— R1→R5 编排主面：Workflow 编排器与 Options 装配面
//（包文档见 doc.go；各阶段面按 r1/stages/compliance/r3tools/r4ckpt/verdict 分文件，
// checkpoint/dispatch/finish 收尾见 workflow_checkpoint/workflow_dispatch/workflow_finish）。

package requirement

import (
	"context"
	"sync"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"

	"github.com/oekit/logx"

	"github.com/yi-nology/argus/internal/argusctx"
	"github.com/yi-nology/argus/internal/dispatch"
	"github.com/yi-nology/argus/internal/domain"
	"github.com/yi-nology/argus/internal/plugin"
	"github.com/yi-nology/argus/internal/semsearch"
	"github.com/yi-nology/argus/internal/toolpool"

	"github.com/oagentkit/textx/codeintel"

	"github.com/oagentkit/llm"
	"github.com/oagentkit/rag"
)

// TaskKind 词表（与 IDL TaskKind 对齐）：canonical 定义自 v3.5.6 上提
// domain.TaskKind*（service/poller 不必为拿常量依赖编排层），本包直接引用。

// Verdict 词表。
const (
	VerdictApprove        = "approve"
	VerdictComment        = "comment"
	VerdictRequestChanges = "request_changes"
)

// Workflow R1→R5 编排器（首个 Workflow 接口实现，§9.4：接口随 v3.0 抽出）。
type Workflow struct {
	// 输入通道契约（v3.54.0 文档化，防三通道漂移；此前勘察在案「八字段外部预写」）：
	//   ① 装配期构造字段——基础设施引用（LLM/Dispatcher/Merger/SDK/Tools/Knowledge），
	//     buildWorkflow 一次构造；
	//   ② 任务期预写字段——MemoryIndex/RepoGuide/ExternalToolCallLimit/OnStage/
	//     CheckpointSink/SASTOutcomes/CodeGraph/CodeSearchDir/RepoConfig：运行期数据
	//     而非装配常量，
	//     runner 每任务 Run 前注入（这正是不用 Options 构造参数表达的原因，v3.54.0 复核定案）；
	//   ③ 调用参数——Run/RunReviewOnly(ctx, task, budget) 携带任务本体与预算。
	// 新增输入先问归属：装配常量→①，任务数据→②，禁止经钩子旁路绕开②。
	LLM llm.Generator
	// R3ChatModel R3 ReAct 专用模型（P6 阶段覆盖；nil = LLM.RawModel()）。
	R3ChatModel model.BaseChatModel
	SDK         *argusctx.SDKCtx
	Dispatcher  *dispatch.Dispatcher
	Merger      *dispatch.Merger
	Tools       toolpool.Pool // v3.0 mock 或 nil
	ToolSpecs   []toolpool.ToolSpec
	Knowledge   rag.KnowledgeService             // v3.1 知识检索（nil/Noop = 不挂 search_knowledge）
	SkillText   func(ctx context.Context) string // R3 方法论注入（runner 装配；nil = 无）

	// MemoryIndex 本仓历史评审记忆索引（v3.35.0，dream 周期导出 + runner 每任务
	// 装载；空 = 无记忆或功能未启用）。感知面：细节全文走 search_knowledge 检索。
	MemoryIndex string

	// SourceReader 证据核验文件读取面（Phase 2 Evidence v2，任务期②注入）：
	// runner 每任务 Run 前绑定 workcopy 池读取器（构造契约=绑定本任务 head），
	// Run 返回后清空（wf 跨任务复用，防 stale 泄漏）；nil = 集合级降级路径。
	SourceReader plugin.SourceReader

	// RepoGuide 仓库级评审规范（v3.38.0，runner 按 repo@sha 从平台 API 装载
	// .argus/REVIEW.md；空 = 未启用或仓库未提供）。半可信输入：已经提醒标签
	// 转义预处理，stages 注入侧带对冲句。
	RepoGuide string

	// llmFailoverBase 本轮 run 起点的 LLM 主备切换计数基线（P2-04 #15 R5.L 门禁
	// 输入；Run 入口记录，judgeAndGate 读差值）。
	llmFailoverBase int64

	// 详情存档目录（v5.0.0 收编 harness spill 语义（今 goagent-kernel），取代 v3.39.0 进程内 map）：
	// 超限工具全文落盘 <WorkRoot>/spill/<taskID>/，get_detail 取回——checkpoint 复活/
	// 进程重启后 ref 不再失效（旧 map 版重启即失）。runner wireWorkflow 每任务注入；
	// 空 = 两级化退纯截断（无取回指引）。
	SpillDir string

	// spillSeq 存档序号分配（spillMu 护并发工具调用的 ref 唯一性）。
	spillMu  sync.Mutex
	spillSeq int

	// OnStage 阶段进度回调（v3.40.0，runner 接线 SSE 发布；nil 安全）。
	OnStage func(stage string)

	// ExternalToolCallLimit 外部 MCP 工具单任务调用上限（v3.41.0 可配；≤0 回退 5）。
	ExternalToolCallLimit int

	// SkillTool 决策使用模式的 use_skill 工具（ARGUS_SKILL_MODE=tool 时装配；
	// 与 SkillText 互斥——静态注入 vs 渐进披露，双开会让正文重复）。
	SkillTool tool.BaseTool
	Log       logx.Logger

	// R1 需求文本截断上限（字符；<=0 用包内缺省）。截断必须留痕：
	// 在 spec.Ambiguous 追加提示 → R5.3 → 人工确认，不允许静默丢内容。
	ReqBodyMaxChars  int
	ReqIssueMaxChars int

	// R3 工具返回内容上限（rune；<=0 用包内缺省）。随模型窗口从 config 派生。
	GetFileMaxChars  int
	GetIssueMaxChars int
	GetDiffMaxChars  int

	// R3.5 报告反思（v3.4，oagentkit/agent/reflection）：>0 启用，值为"批判→修订"轮数上限
	//（包内钳制 ≤ reflectMaxRounds）。ReflectModel 为反思专用模型（nil = R3ChatModel
	// → LLM.RawModel() 逐级回退）。ReflectEvidence 证据门：off|soft|strict（默认 off）。
	ReflectRounds   int
	ReflectModel    model.BaseChatModel
	ReflectEvidence string

	// G4 完备性裁判（v3.11.0；默认 demote 自 v6.3.0）：demote（默认，APPROVE→COMMENT）|
	// demote（任一检查不过时 APPROVE→COMMENT，rule=R5.C）；CompletenessRetry =
	// 受限补审（失败插件在剩余预算内重试一次，结果替换不叠加）。
	CompletenessPolicy string
	CompletenessRetry  bool

	// CheckpointSink 阶段产物落库钩子（v3.12.0 G2 断点续审；nil = 不落）：
	// R1/R2 成功完成后即时回调，runner 持久化到 tasks.stage_artifacts；
	// 同任务重试时经 task.Artifacts 的 checkpoint_* 键回流，跳过已成功阶段。
	CheckpointSink func(artifacts map[string]string)

	// RepoConfig 仓库主权配置（v3.15.0：runner 单次加载后注入，min_severity 等
	// review 段行为在 finish 内消费）。nil = 未注入（loadRepoConfig 自行加载，
	// 既有测试路径不受影响）。
	RepoConfig *plugin.RepoConfig
	// SASTOutcomes 确定性 SAST 前置产出（v3.15.0：runner 在工作副本上跑
	// gitleaks/semgrep 后注入；finish 内并入 fan-out outcomes 一起合并）。
	SASTOutcomes []dispatch.Outcome
	// CodeGraph Go 符号索引（v3.15.0 P0-1：runner 工作副本上建好后注入；
	// 非 nil 时 r3Tools/R4 ReviewTools 挂 get_symbol/get_references 工具）。
	CodeGraph *codeintel.Index
	// Graph 消歧符号图（v5.11.0；nil=未启用）：runner injectCodeGraph 在工作副本
	// 上以 semsearch.NewIndex 建好后注入——缺省纯图模式（nil embedder，图查询零
	// embedding 依赖，无 ARGUS_EMBEDDING_* 部署同样可用，不被绑架）；v6.1.0 起
	// ARGUS_SEMSEARCH 就绪时注入 embedder（Searchable()=true），r3Tools/R4
	// ReviewTools 挂 get_trace/get_symbol_context（非 nil 即挂）与 search_sem
	// （Searchable 才挂）；影响面与 get_callers 词法面互补（与 CodeGraph 独立
	// 判空，互不绑定）。
	Graph *semsearch.Index
	// CodeSearchDir 工作副本根路径（v3.84.0：runner 就绪后注入；非空时 r3Tools/R4
	// ReviewTools 挂 search_code 全文检索工具，覆盖符号索引到不了的词法线索）。
	// 池语义：注入只记路径不持引用，目录由 workcopy TTL 兜底存活；工具侧读不到
	// 时按工具错误文本降级，不中断 ReAct。
	CodeSearchDir string

	// ckptMu R4 逐插件 checkpoint 落库序列化（v3.154.0）：
	// MergeTaskStageArtifacts 是读-改-写合并，fan-out 并发落键不加锁会丢更新。
	// 任务级实例锁，与 R1/R2 的顺序落库互不竞争。
	ckptMu sync.Mutex
}

// skillsForKind 任务类型 → R4 分派的技能词表。
func skillsForKind(kind string) []string {
	switch kind {
	case domain.TaskKindVisualCheck:
		return []string{plugin.SkillVisualCheck}
	default:
		return []string{plugin.SkillCodeReview}
	}
}

// Run 完整链路 R1→R5（requirement_compliance 任务）。
func (w *Workflow) Run(ctx context.Context, task plugin.Task, budget *llm.Budget) (*plugin.WorkflowOutput, error) {
	return run(w, ctx, task, budget)
}

// RunReviewOnly 仅 R4+R5（code_review / visual_check 任务：无需求清单，R5.1/R5.3 需求条件跳过）。
func (w *Workflow) RunReviewOnly(ctx context.Context, task plugin.Task, budget *llm.Budget) (*plugin.WorkflowOutput, error) {
	return runReviewOnly(w, ctx, task, budget)
}
