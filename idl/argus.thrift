// argus HTTP API IDL —— 路由唯一事实来源（docs/design/design.md §5）。
// v6.3.0 拆分：类型（enum/struct）按域拆入域文件，service 全部声明在 master（hz
// 只为 --idl 顶层文件的 service 生成路由，探针实锤；域文件共享单一 namespace
// go argus），model 按域文件拆、handler/router 归 biz/{handler,router}/argus/（此前 */api/）。
//
// 改路由后必跑：`make idl-gen`（hz update），生成物严禁手改（"DO NOT EDIT"
// 文件手改一次 hz update 即失真——TaskKind 枚举/duplicated 字段手改前科）；
// CI 有 idl-drift 门禁（hz update 后 git diff --exit-code），手改生成物必红。
// 生成物手写区（hz 保留、不被重写）：biz/handler/argus/*_service.go 函数体、
// biz/router/argus/middleware.go、biz/handler/argus/respond.go。
//
// 刻意不进 IDL 的路由（均有设计背书，勿私挂业务端点效仿）：
//   /healthz /readyz /version /metrics —— 基础设施探针/可观测（main.go registerCustom）
//   /mcp、/.well-known/agent.json      —— MCP streamable http 与 A2A 卡片，规范要求裸协议体
// /events 已于 v6.3.0 收编（task.thrift EventsService，响应空 struct、handler 体
// 保持 SSE 流式实现）；/ping（hz 脚手架遗留）已于 v3.189.0 删除。
//
// include 规则：域文件必须自行 include 它引用的类型文件（thrift 作用域无传递性），
// 共享 namespace（namespace go argus）下 thrift include 前缀只存在于 IDL 源，
// 生成的 Go 同包无前缀。
namespace go argus

include "task.thrift"
include "agents.thrift"
include "findings.thrift"
include "repo_settings.thrift"
include "webhook.thrift"
include "controlplane.thrift"
include "merge.thrift"
include "fix_approval.thrift"
include "fix_push.thrift"
include "auth.thrift"
include "ask.thrift"
service TaskService {
    task.CreateTaskResponse CreateTask(1: task.CreateTaskRequest request) (api.post="/tasks")
    task.GetTaskResponse GetTask(1: string taskId (api.path="taskId")) (api.get="/tasks/:taskId")
    task.ListTasksResponse ListTasks(1: task.ListTasksRequest request) (api.get="/tasks")
    // —— 2026-10-27 收编的手工注册业务面（原 router.go/main.go customizedRegister）——
    // 按 head SHA 查最近一次成功报告（v3.1），响应复用 task.GetTaskResponse。
    task.GetTaskResponse ReportByRef(1: string ref (api.path="ref")) (api.get="/reports/:ref")
    // 本月各仓库 token 用量与预算水位（v3.1）。
    task.UsageMonthlyResponse UsageMonthly() (api.get="/usage/monthly")
    // 审查概览聚合（v3.88.0，按仓任务量/verdict 分布/抑制计数）。
    task.StatsOverviewResponse StatsOverview(1: task.StatsOverviewRequest request) (api.get="/stats/overview")
    // 任务影响面交互视图（v3.183.0）。
    task.TaskImpactResponse TaskImpact(1: string taskId (api.path="taskId")) (api.get="/tasks/:taskId/impact")
    // 仓库级代码地图（v5.6.2）：整仓目录依赖图自包含交互 HTML（浅克隆+TTL 缓存）。
    task.RepoMapResponse RepoMap(1: string owner (api.query="owner"), 2: string repo (api.query="repo"), 3: optional string platform (api.query="platform")) (api.get="/repos/map")
    // 健康报告查询面（v3.13.0，与周期 IM 推送同数据源）。
    task.HealthReportResponse HealthReport() (api.get="/reports/health")
    // 质量控制台（#27，v7.1 生产运营）：误报/插件表现/Token 消费/失败原因
    // 聚合 HTML 页——数据来自既有 stats/usage/Registry 面，零新表。
    task.QualityConsoleResponse QualityConsole() (api.get="/console")
}

service FindingFeedbackService {
    findings.SuppressFindingResponse SuppressFinding(1: findings.SuppressFindingRequest request) (api.post="/findings/suppress")
    // 恢复被抑制意见（v3.131.0）：admin 档 token（service 层 scope 二次判定，fail-closed）。
    findings.RestoreFindingResponse RestoreFinding(1: findings.RestoreFindingRequest request) (api.post="/findings/restore")
    // 意见治理台（v8.5.0）：按仓跨 PR 列 finding_prints（read 档 + 仓库维度判定）。
    // 响应动态 JSON（findings/total），IDL 只承载路由契约。
    findings.ListFindingsResponse ListFindings(1: string platform (api.query="platform"),
        2: string owner (api.query="owner"),
        3: string repo (api.query="repo"),
        4: optional string status (api.query="status"),
        5: optional i32 limit (api.query="limit")) (api.get="/findings")
}

service WebhookService {
    webhook.WebhookResponse Receive(1: string platform (api.path="platform")) (api.post="/webhook/:platform")
}

// —— v3.189.0 收编：v3.187.0 散落 router.go 手挂的按仓配置路由（原
// customizedRegister 四行）——
service RepoSettingsService {
    // 全部按仓配置（观测面，read 档）。
    repo_settings.RepoSettingsResponse RepoSettingsList() (api.get="/repo-settings")
    // 单仓配置视图（覆盖集 + 全局生效值，read 档）。
    repo_settings.RepoSettingsResponse RepoSettingsGet(1: string platform (api.path="platform"),
        2: string owner (api.path="owner"),
        3: string repo (api.path="repo")) (api.get="/repo-settings/:platform/:owner/:repo")
    // 写入单仓覆盖（admin 档由 requiredScope 收口）：body 为 reposettings.Settings
    // JSON 原始字节透传（DisallowUnknownFields 严格校验在 service 层 Parse，
    // 不经 IDL struct 绑定），上限 4KB。
    repo_settings.RepoSettingsResponse RepoSettingsPut(1: string platform (api.path="platform"),
        2: string owner (api.path="owner"),
        3: string repo (api.path="repo")) (api.put="/repo-settings/:platform/:owner/:repo")
    // 移除单仓覆盖（admin 档；幂等）。
    repo_settings.RepoSettingsResponse RepoSettingsDelete(1: string platform (api.path="platform"),
        2: string owner (api.path="owner"),
        3: string repo (api.path="repo")) (api.delete="/repo-settings/:platform/:owner/:repo")
}

// —— ListAgents 由 TaskService 归位 AgentService（v6.3.0 拆分）——
// ShareService share 面运维（#91）：令牌撤销。独立 service 块——hz update
// 对既有 service 的新方法不补路由（模型照生成，路由静默跳过，实锤），新
// service 块则全量生成（RepoSettingsService 先例）。
service ShareService {
    // 撤销 share 令牌（admin 档由 /controlplane 前缀收口 requiredScope）。
    // subject = 任务 ID（影响面域）或 "platform:owner/repo"（repo-map 域）。
    controlplane.ControlPlaneRevokeShareResponse RevokeShare(
        1: controlplane.ControlPlaneRevokeShareRequest request)
        (api.post="/controlplane/shares/revoke")
}

service AgentService {
    agents.ListAgentsResponse ListAgents(1: agents.ListAgentsRequest request) (api.get="/agents")
}

// —— v6.3.0 收编（原 router.go customizedRegister 手挂）——
service EventsService {
    // /events SSE 任务进度流（v3.1）。
    // v6.3.0 收编进 IDL：响应空 struct 仅作路由声明锚点，
    // handler 体保持 SSE 流式实现。
    task.EventsResponse Events() (api.get="/events")
}

// —— #48 控制面人工介入（admin-only；取 kernel controlplane「模式」，类型本域
// 定义，不把 kernel Plane/taskboard/tenant 绑进域模型）——
service ControlPlaneService {
    // 列出 uncertain 副作用（key/resource/reason/updated_at/retry_safety）。
    controlplane.ControlPlaneListEffectsResponse ListUncertainEffects()
        (api.get="/controlplane/side-effects")
    // 人工裁决 unknown（applied/not-applied + force_retryable）。
    controlplane.ControlPlaneResolveResponse ResolveUncertainEffect(
        1: controlplane.ControlPlaneResolveRequest request)
        (api.post="/controlplane/side-effects/resolve")
    // 列出活跃 side-effect 资源租约（resource/holder/fence/expires_at）。
    controlplane.ControlPlaneListLeasesResponse ListLeases()
        (api.get="/controlplane/leases")
    // 强制释放资源租约（资源行 + 该资源未裁决执行意图）。
    controlplane.ControlPlaneReleaseLeaseResponse ReleaseLease(
        1: controlplane.ControlPlaneReleaseLeaseRequest request)
        (api.post="/controlplane/leases/release")
    // 终止任务（立即 failed、取消自动重试；在途执行在检查点停止产生副作用）。
    controlplane.ControlPlaneCancelTaskResponse CancelTask(
        1: controlplane.ControlPlaneCancelTaskRequest request)
        (api.post="/controlplane/tasks/cancel")
}

// —— 受控合并（T2 合并资格门；admin-only，preview→confirm 两阶段）——
// 独立 service 块：hz update 对既有 service 新方法不补路由（argus.thrift 注释
// 实锤），新 service 块全量生成（ShareService/RepoSettingsService 先例）。
service MergeService {
    // 只读资格查询（不建操作、不写远端；fail-closed 判定全量透出）。
    merge.MergeEligibilityResponse MergeEligibility(
        1: merge.MergeEligibilityRequest request)
        (api.get="/merges/eligibility")
    // 创建合并候选：资格门通过才落 pending op 并绑定 expected_remote_head。
    merge.MergePreviewResponse MergePreview(
        1: merge.MergePreviewRequest request)
        (api.post="/merges/preview")
    // 确认合并：重读远端 + 经副作用闸执行 MergeCR；绑定 head 不一致拒绝。
    merge.MergeConfirmResponse MergeConfirm(
        1: merge.MergeConfirmRequest request)
        (api.post="/merges/confirm")
    // 查询合并操作状态。
    merge.MergeGetResponse MergeGet(1: string opId (api.path="opId"))
        (api.get="/merges/:opId")
    // 读回对账 uncertain 操作（平台实际状态幂等定案）。
    merge.MergeReconcileResponse MergeReconcile(
        1: merge.MergeReconcileRequest request)
        (api.post="/merges/reconcile")
}

// —— 高危修复审批票（T3；admin-only）——
service FixApprovalService {
    // 按 PR 列审批票（懒生成：请求时 Ensure 当前 head 高危票）。
    fix_approval.FixApprovalListResponse ListFixApprovals(
        1: fix_approval.FixApprovalListRequest request)
        (api.get="/fix-approvals")
    // 裁决一张 pending 票（approved | rejected）。
    fix_approval.FixApprovalDecideResponse DecideFixApproval(
        1: fix_approval.FixApprovalDecideRequest request)
        (api.post="/fix-approvals/decide")
}

// —— 修复推送 preview→confirm（T4；ARGUS_FIX_PUSH=confirm）——
service FixPushService {
    // 只读预览：任务就绪态 + 待审批票数（不执行远端写）。
    fix_push.FixPushPreviewResponse FixPushPreview(
        1: fix_push.FixPushPreviewRequest request)
        (api.get="/fix-push/preview")
    // 确认推送：绑定 task_id + expected_remote_head（陈旧拒绝；经审批门+副作用闸）。
    fix_push.FixPushConfirmResponse FixPushConfirm(
        1: fix_push.FixPushConfirmRequest request)
        (api.post="/fix-push/confirm")
}

// —— 多用户最小集（T5：登录/邀请/权限投影）——
service AuthService {
    // 登录（公开；签发会话 token，scope=角色投影）。
    auth.AuthLoginResponse AuthLogin(1: auth.AuthLoginRequest request)
        (api.post="/auth/login")
    // 登出（撤销当前 Bearer 会话）。
    auth.AuthLogoutResponse AuthLogout(1: auth.AuthLogoutRequest request)
        (api.post="/auth/logout")
    // 当前身份 + my_permissions（需登录）。
    auth.AuthMeResponse AuthMe()
        (api.get="/auth/me")
    // 创建邀请（admin；永不授予 admin 角色）。
    auth.AuthInviteResponse AuthInvite(1: auth.AuthInviteRequest request)
        (api.post="/auth/invites")
    // 接受邀请（公开）。
    auth.AuthAcceptInviteResponse AuthAcceptInvite(1: auth.AuthAcceptInviteRequest request)
        (api.post="/auth/accept-invite")
}

// —— 审查问答 HTTP 面（与 MCP ask_argus 同源；read 档）——
service AskService {
    // 最新结论（question 空）或基于报告追问（question 非空）。
    ask.AskReviewResponse AskReview(1: ask.AskReviewRequest request)
        (api.post="/ask")
    // GET 语义糖：只取最新审查结论。
    ask.AskReviewResponse AskLatest(1: ask.AskLatestRequest request)
        (api.get="/ask/review")
}
