// service_authorizer.go —— 统一 Authorizer（v6.7.0 P0-02，#3）。
//
// 鉴权两层此前拆散：scope 档位（BearerMiddleware 按 method+path 判）+ 仓库约束
// （TokenReposFromContext 散落消费）。后者仅在部分读面生效——reports/stats/
// repomap/repo-settings 四读面漏接，持任意 read 档 token 即可跨仓读取完整审查
// 报告（service_repo_guard.go:47 的设计意图「单 token 泄露半径压到其 pattern
// 内」在读面失效）。
//
// 本文件是仓库维度的单点判定：HTTP / MCP / SSE 三出口的全部数据面都经这里。
//   - HTTP/MCP：各 service 方法入口判定（同一方法两出口天然共用）；
//   - SSE：/events handler 对每条事件判定（AuthorizeEvent）。
//
// 授予形态（grant）：Bearer token（scope+仓库约束由 BearerMiddleware 写入 ctx）
// / share HMAC 令牌（单资源绑定，中间件放行即授权该资源）/ webhook 平台签名
// 面（受信路径，不入 Authorizer）。share 与 webhook 不进 Bearer ctx，判定按
// 「无仓库约束 = 该 grant 已在更早环节绑定资源」处理——share 令牌是单资源
// HMAC，webhook 有签名校验，泄露半径由各自机制承担。

package service

import (
	"context"
	"errors"

	"github.com/yi-nology/argus/internal/obs"
)

// Authorize 仓库维度单点判定（v6.7.0 P0-02）：token 仓库约束（pattern）+
// 全局 ARGUS_REPO_ALLOWLIST 双门（repoAllowedFor 语义并入，读面此前漏接的
// 判定全部经本入口）。返回 false = 调用方必须拒绝（HTTP 403 / MCP 拒绝态 /
// SSE 过滤丢弃）。
func (s *Service) Authorize(ctx context.Context, platform, owner, repo string) bool {
	return authorize(s, ctx, platform, owner, repo)
}

// AuthorizeTask task_id 维度读取判定：token 带仓库约束时，task_id 直读
// （get_report/stages/findings/suggestion/impact/reports-by-ref）只能触达约束内
// 仓库的任务。platform=local（无仓语义任务）按「持有 task_id ≈ 持有能力」放行
// （taskVisible 语义并入）。
func (s *Service) AuthorizeTask(ctx context.Context, platform, owner, repo string) bool {
	return authorizeTask(s, ctx, platform, owner, repo)
}

// AuthorizeEvent SSE 事件出口的行级判定（/events handler 逐事件调用）：
// token 仓库约束外的任务事件不可见——跨实例事件总线（Outbox）投递的行同样
// 经此过滤，事件源不影响可见性。
// AuthorizeEvent SSE 事件出口的行级判定（/events handler 逐事件调用；
// evTenant 事件携带的租户归属字段，#105）。
func (s *Service) AuthorizeEvent(ctx context.Context, platform, owner, repo, evTenant string) bool {
	return authorizeEvent(s, ctx, platform, owner, repo, evTenant)
}

// AuthorizeRowFilter 聚合观测面（stats/usage/repo-settings 清单）的行级过滤器：
// token 带仓库约束时约束外仓库的行不可见（此前全量透出=跨仓元数据泄露）；
// 无约束 token 全量。返回 nil = 不过滤（调用方免逐行判定开销）。
func (s *Service) AuthorizeRowFilter(ctx context.Context) func(platform, owner, repo string) bool {
	return authorizeRowFilter(s, ctx)
}

// authzDeniedMetric P2-05（#16）授权拒绝计数（route×scope 维度）：约束外访问
// 的可观测面——泄露尝试/误配 token 的信号。
func authzDeniedMetric(ctx context.Context, route string) {
	obs.IncrAuthzDenied(ctx, route, ScopeFromContext(ctx))
}

// ErrAuthzDenied Authorize 拒绝的统一哨兵：HTTP handler 以 errors.Is 映射 403
// （区别于上游故障的 502/500）。MCP 面直接透出文案。
var ErrAuthzDenied = errors.New("仓库不在 token 仓库约束范围内")

// ---------- 路由授权矩阵（防漂移单源） ----------

// 授权门词表：仓库维度判定要求。与 IDL 路由一一对应（TestRouteAuthzMatrixCoversIDL
// 钉住「IDL 每条非白名单路由都有矩阵行」），TestAuthzRepoDenialMatrix 按行为面
// 钉住「矩阵声明判定的端点跨仓必 403/过滤」。
const (
	AuthzGateRepo        = "repo"   // 仓库三元组判定：Authorize → 越权 403
	AuthzGateTask        = "task"   // task_id/ref 维度判定：AuthorizeTask → 越权 403
	AuthzGateRowFilter   = "rows"   // 聚合清单行过滤：AuthorizeRowFilter → 越权行剔除
	AuthzGateEventFilter = "events" // SSE 事件流逐事件过滤：AuthorizeEvent
	AuthzGateExempt      = "exempt" // 赦免（Note 说明理由）
)

// routeAuthzMatrix 路由 → 授权门矩阵（P0-02 防漂移单源）。scope 档位由
// requiredScope（service_auth.go）判，本矩阵只管仓库维度。赦免名单：
//   - GET /reports/health —— 服务端运行健康（无仓库数据维度）；
//   - GET /agents —— Agent 词表元数据（非仓库数据）；
//   - /controlplane/* —— 控制面人工介入（全局面，无仓库维度；admin 档由
//     requiredScope 前缀收口）；
//   - review_diff / review_diff_async 同理（本地 diff 无仓语义，MCP 面）。
//
// RouteAuthzRow 路由授权矩阵行（AuthzMatrix 导出供门禁测试消费）。
type RouteAuthzRow struct{ Method, Path, Gate, Note string }

var routeAuthzMatrix = []RouteAuthzRow{
	{"GET", "/events", AuthzGateEventFilter, "SSE 事件按 token 仓库约束过滤（P0-02）"},
	{"GET", "/tasks", AuthzGateRowFilter, "清单行过滤 taskListable（v3.140.0 已接，入矩阵防回退）"},
	{"GET", "/tasks/:taskId", AuthzGateTask, "taskVisible（v3.140.0 已接）"},
	{"GET", "/tasks/:taskId/impact", AuthzGateTask, "taskVisible（service_impact 已接）"},
	{"GET", "/tasks/:taskId/stages", AuthzGateTask, "taskVisible（v8.6 阶段时间线，service_agents 已接）"},
	{"POST", "/tasks", AuthzGateRepo, "repoAllowedFor（建任务写面已接）"},
	{"POST", "/findings/suppress", AuthzGateRepo, "仓库三元组判定（v6.7.0 补：HTTP 写面此前无仓库维度）"},
	{"POST", "/findings/restore", AuthzGateRepo, "仓库三元组判定（v6.7.0 补，同 suppress）"},
	{"GET", "/findings", AuthzGateRepo, "意见治理台按仓清单（v8.5.0，方法内 Authorize）"},
	{"GET", "/reports/health", AuthzGateExempt, "服务端运行健康，无仓库数据维度"},
	{"GET", "/reports/:ref", AuthzGateTask, "按 SHA 解析任务后 taskVisible（v6.7.0 补：跨仓报告读取缺口）"},
	{"GET", "/repos/map", AuthzGateRepo, "repoAllowedFor（v6.7.0 补：跨仓地图渲染缺口）"},
	{"GET", "/repos/branches", AuthzGateRepo, "分支清单（v8.8.0；方法内 Authorize）"},
	{"GET", "/repos/crs", AuthzGateRepo, "PR/MR 列表（v8.8.1；方法内 Authorize）"},
	{"GET", "/stats/overview", AuthzGateRowFilter, "聚合行过滤（v6.7.0 补：跨仓统计元数据缺口）"},
	{"GET", "/console", AuthzGateRowFilter, "质量控制台聚合 stats/usage（#27；行过滤在 StatsOverview/UsageMonthly 内）"},
	{"GET", "/console/options", AuthzGateRowFilter, "控制台选项聚合（v8.7.0）：词表全量透出 + repos 行过滤（租户谓词 + AuthorizeRowFilter）"},
	{"GET", "/usage/monthly", AuthzGateRowFilter, "聚合行过滤（v6.7.0 补：跨仓用量元数据缺口）"},
	{"GET", "/repo-settings", AuthzGateRowFilter, "清单行过滤（v6.7.0 补）"},
	{"GET", "/repo-settings/:platform/:owner/*repo", AuthzGateRepo, "仓库三元组判定（v6.7.0 补）"},
	{"PUT", "/repo-settings/:platform/:owner/*repo", AuthzGateRepo, "admin 档之上的仓库维度（约束内 admin 才可写）"},
	{"DELETE", "/repo-settings/:platform/:owner/*repo", AuthzGateRepo, "admin 档之上的仓库维度"},
	// —— #48 控制面人工介入：仓库维度在方法内判定（#60 两级语义：无约束
	// admin=system_admin 全局；admin@pattern=repo_admin 仅约束内可见/可操作，
	// 列表行过滤 + 单资源 403 + token 指纹审计）——
	{"GET", "/controlplane/side-effects", AuthzGateExempt, "控制面 admin；repo_admin 行过滤（#60）"},
	{"POST", "/controlplane/side-effects/resolve", AuthzGateExempt, "控制面 admin；资源归属判定 403（#60）"},
	{"GET", "/controlplane/leases", AuthzGateExempt, "控制面 admin；repo_admin 行过滤（#60）"},
	{"POST", "/controlplane/leases/release", AuthzGateExempt, "控制面 admin；资源归属判定 403（#60）"},
	{"POST", "/controlplane/tasks/cancel", AuthzGateExempt, "控制面 admin；AuthorizeTask 仓库维度（#60）"},
	{"POST", "/controlplane/shares/revoke", AuthzGateExempt, "控制面 admin；share 撤销（#91）"},
	{"GET", "/agents", AuthzGateExempt, "Agent 词表元数据，非仓库数据（task 维度子资源已各自判定）"},
	// —— T2 受控合并：坐标在查询串/body，仓库维度在方法内 Authorize 收口 ——
	{"GET", "/merges/eligibility", AuthzGateRepo, "合并资格只读查询（方法内 Authorize）"},
	{"POST", "/merges/preview", AuthzGateRepo, "合并候选预检（方法内 Authorize）"},
	{"POST", "/merges/confirm", AuthzGateExempt, "确认合并：仓库维度从 op 记录解析（方法内 Authorize）"},
	{"GET", "/merges/:opId", AuthzGateExempt, "合并操作查询：仓库维度从 op 记录解析"},
	{"POST", "/merges/reconcile", AuthzGateExempt, "合并读回对账：仓库维度从 op 记录解析"},
	// —— T3 修复审批票：坐标在查询串/body，仓库维度方法内收口 ——
	{"GET", "/fix-approvals", AuthzGateRepo, "列审批票（方法内 Authorize）"},
	{"POST", "/fix-approvals/decide", AuthzGateExempt, "裁决审批票：仓库维度从票记录解析"},
	// —— T4 修复推送 preview→confirm ——
	{"GET", "/fix-push/preview", AuthzGateRepo, "推送就绪预览（方法内 Authorize）"},
	{"POST", "/fix-push/confirm", AuthzGateExempt, "确认推送：仓库维度从任务记录解析"},
	// —— 审查问答（read 档；仓库维度方法内收口）——
	{"POST", "/ask", AuthzGateRepo, "审查问答（方法内 Authorize）"},
	{"GET", "/ask/review", AuthzGateRepo, "最新结论（方法内 Authorize）"},
	// —— T5 多用户：登录/接受邀请公开（不挂 authMw）；其余会话面 ——
	{"POST", "/auth/login", AuthzGateExempt, "公开登录"},
	{"POST", "/auth/accept-invite", AuthzGateExempt, "公开接受邀请"},
	{"POST", "/auth/logout", AuthzGateExempt, "登出（会话维度，无仓）"},
	{"GET", "/auth/me", AuthzGateExempt, "身份+权限投影（无仓）"},
	{"GET", "/auth/members", AuthzGateExempt, "成员清单（v8.8.0；admin 角色在方法内判定，无仓）"},
	{"POST", "/auth/invites", AuthzGateExempt, "创建邀请（admin 角色在方法内判定）"},
	{"GET", "/auth/invites/history", AuthzGateExempt, "邀请历史（v8.8.2；admin 角色在方法内判定，无仓）"},
}

// AuthzMatrix 路由授权矩阵（只读导出：e2e 门禁测试与 IDL 路由清单对账用）。
func AuthzMatrix() []RouteAuthzRow { return routeAuthzMatrix }
