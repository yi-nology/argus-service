package service

import (
	"context"
	"net/http"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/yi-nology/go-git-platform/model"

	argus "github.com/yi-nology/argus/biz/model/argus"
)

// ---------- webhook（POST /webhook/:platform）----------

// webhook 回执 Status wire 词表（v3.121.0 常量化：此前 14 处结构体字面量散写，
// API/SSE 消费方按此分发——字面量漂移即静默契约断裂）。构造统一走 whResp。
const (
	whAccepted  = "accepted"
	whBusy      = "busy"
	whDuplicate = "duplicate"
	whSkipped   = "skipped"
	whError     = "error"
)

// whResp webhook 回执构造工厂（Status 词表单源 + Reason 统一指针化；带
// EventId/TaskId 等附加字段的调用方在拿到回执后补字段）。
func whResp(status, reason string) *argus.WebhookResponse {
	return &argus.WebhookResponse{Status: status, Reason: strPtr(reason)}
}

// 事件类型白名单（§5.1 规则 1）。SDK 各后端在投递前都会过 runtime.NormalizeCRAction
// 归一化（opened/closed/merged/reopened/updated 五词，未识别词原样透传），因此键必须是
// 归一化词表而非平台原生词：v3.7.x 及此前维护的是原生词表（cr.synchronize/cr.update 等），
// GitHub 的 synchronize、GitLab 的 update 归一化为 cr.updated 后不在白名单——"推新提交
// 触发重审"整条静默断流，Forgejo 的 cr.synchronized 命中纯靠透传拼写巧合（v3.8.0 修复）。
// cr.merged / cr.closed 移出白名单：对已合并/已关闭的 MR 发起全量审查只烧 token。
var crEventWhitelist = map[string]string{
	"cr.opened":       "opened",
	"cr.reopened":     "reopened",
	"cr.updated":      "updated",      // 所有平台"推新提交/编辑"的归一化形态
	"cr.synchronized": "synchronized", // 历史兼容：未过归一化的直连投递仍可触发
	"cr.approved":     "approved",     // 透传词：评审动作触发复审的平台接入点
	"cr.unapproved":   "unapproved",
}

// Webhook 平台 webhook 入口：返回 (IDL 响应体, HTTP 状态码)。
// Webhook webhook 入口。v3.58.0 拆分为准入防线（webhookAdmission）→ 事件分流
// （installation/comment/CR）→ CR 过滤链（webhookCRFilter）→ 建任务去重
// （webhookCreateTask）。§5.1 过滤规则顺序即安全语义，不得重排。
func (s *Service) Webhook(ctx context.Context, c *app.RequestContext, platform string) (*argus.WebhookResponse, int) {
	return webhook(s, ctx, c, platform)
}

// webhookAdmission 准入防线（顺序即安全语义，不得重排）：平台接入校验 →
// secret fail-closed → Content-Length 预检 → 签名校验+事件解析 → 幂等预查。
// ok=false 时 resp/status 即最终响应。
// webhookAdmission 返回首参为租户归属 ctx（#82：坐标反查 tenant_repos 映射，
// 下游任务/评论/抑制/幂等全按该租户走存储谓词；无映射 = default 租户）。
func (s *Service) webhookAdmission(ctx context.Context, c *app.RequestContext, platform string) (
	ctxOut context.Context, ev *model.NormalizedEvent, delivery string, resp *argus.WebhookResponse, status int, ok bool) {
	return webhookAdmission(s, ctx, c, platform)
}

// webhookCRFilter §5.1 CR 事件过滤链：规则 1 事件类型白名单（防评论风暴、防
// 自我触发循环）→ 规则 2 bot 自审保护 → 规则 3 draft 默认不审 + 缺 CR/Repo
// 信息的异常事件防御性跳过（长跑服务不 panic）。ok=false 时 resp/status 即
// skipped 响应。
func (s *Service) webhookCRFilter(ev *model.NormalizedEvent,
	skipped func(reason, why string) (*argus.WebhookResponse, int)) (*argus.WebhookResponse, int, bool) {
	return webhookCRFilter(s, ev, skipped)
}

// dupResp 幂等去重回执模板（v3.109.0，此前 4 处近逐字）：duplicate 状态 +
// 原因文案，existingID 非空时回填指向既有任务（store 回查失败时不指幻影 ID）。
func dupResp(reason, existingID, delivery string) (*argus.WebhookResponse, int) {
	resp := whResp(whDuplicate, reason)
	resp.EventId = strPtr(delivery)
	if existingID != "" {
		resp.TaskId = strPtr(existingID)
	}
	return resp, http.StatusOK
}

// webhookCreateTask 组装任务记录（Kind 固定走需求复核 workflow；PR Bot 形态）
// 并落库：规则 4 同 head SHA 去重（查询与落库同一事务，防 in-flight 竞态/
// TOCTOU）→ 去重分支透出 → 审计 + SSE 发布。
func (s *Service) webhookCreateTask(ctx context.Context, ev *model.NormalizedEvent,
	delivery, platform string) (*argus.WebhookResponse, int) {
	return webhookCreateTask(s, ctx, ev, delivery, platform)
}

// pipelineCreateTask CI 失败事件 → ci_analysis 任务（写作面归因）。
// 去重键=同 commit 失败只归因一次（last_head_sha 语义复用：pipeline 对同
// SHA 只有一个终态，重复投递靠 event_id 幂等兜底）。Number 可空——无 MR 的
// push pipeline 归因产物只落库不评论。
func (s *Service) pipelineCreateTask(ctx context.Context, ev *model.NormalizedEvent,
	delivery, platform string) (*argus.WebhookResponse, int) {
	return pipelineCreateTask(s, ctx, ev, delivery, platform)
}
