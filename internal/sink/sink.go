// Package sink 审查结果上报（push 模型旁路）：审查完成后把任务 + 合并后 findings
// POST 给 review-service 的承接端点（POST /api/v1/argus/reviews，契约见
// review-service biz/handler/argus_service），供其前端「Argus 审查」页展示。
//
// 旁路纪律：上报失败只告警不阻断审查主流程——调用方（runner）用短超时 ctx +
// 后台 goroutine 触发，错误不回流审查管线。
package sink

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/oekit/concurrency/async"
	"github.com/oekit/logx"
	"github.com/oekit/pkg/httpjson"

	"github.com/yi-nology/argus/internal/domain"
	"github.com/yi-nology/argus/internal/outbound"
	"github.com/yi-nology/argus/internal/plugin"
	"github.com/yi-nology/argus/internal/tenant"
)

// 硬上限：payload 意见条数与单次超时（超限截断并标记，防异常刷爆对端）。
// URL 来自部署方配置（ARGUS_SINK_URL，运维信任源），非用户可控输入。
const (
	MaxFindings    = 500
	defaultTimeout = 10 * time.Second
)

// Route 单租户承接端点覆盖（#97）。
type Route struct {
	URL   string
	Token string
}

// Reporter 结果上报器（nil = 关闭）。
type Reporter struct {
	URL   string // 承接端点，如 http://review-service:8000/api/v1/argus/reviews
	Token string // Bearer 凭证（ARGUS_SINK_TOKEN；空 = 不带 Authorization 头）
	Log   logx.Logger
	// Routes 租户路由表（tenant ID → 端点；#97）。Report 按 ctx 租户解析：
	// 命中路由用路由端点，否则回落全局 URL。nil = 全局单端点形态（兼容）。
	Routes map[string]Route
	// client 可选注入（测试观测调用点用）；nil = 每次上报按当前 outbound
	// 设置惰性装配可信档客户端。
	client *http.Client
}

// httpClient 返回本次上报使用的客户端。
//
// 未注入时每次现装配（而不是构造 Reporter 时缓存）：bootstrap 的
// outbound.ConfigureFromEnv 可能晚于 Reporter 构造，惰性读取才能让
// ARGUS_OUTBOUND_MODE=off 等运行期配置在首次上报时生效；包级/构造期缓存
// 会冻结构造时刻的默认 warn + nil logger，回滚开关形同虚设。上报是低频
// 旁路，每次新建客户端的成本可忽略。
func (r *Reporter) httpClient() *http.Client {
	if r.client != nil {
		return r.client
	}
	return newHTTPClient()
}

// NewReporter 构造上报器（bootstrap 装配用）。
func NewReporter(url, token string, log logx.Logger) *Reporter {
	return &Reporter{URL: url, Token: token, Log: log}
}

// Payload 上报契约（与 review-service ingestReq 同构）。
type Payload struct {
	ArgusTaskID string    `json:"argus_task_id"`
	Platform    string    `json:"platform"`
	Owner       string    `json:"owner"`
	Repo        string    `json:"repo"`
	Number      string    `json:"number"`
	Title       string    `json:"title"`
	HeadSHA     string    `json:"head_sha"`
	BaseBranch  string    `json:"base_branch,omitempty"`
	Verdict     string    `json:"verdict"`
	Findings    []Finding `json:"findings"`
	Plugins     []string  `json:"plugins,omitempty"`
	DurationMS  int64     `json:"duration_ms,omitempty"`
	TokenUsage  int       `json:"token_usage,omitempty"`
	OccurredAt  time.Time `json:"occurred_at"`
	// Tenant 租户归属（#97/#110：承接端按租户路由/分账的元数据）。
	Tenant string `json:"tenant,omitempty"`
}

// Finding 单条意见（severity 为 argus 归一化词表，对端校验同词表）。
type Finding struct {
	FP           string `json:"fp,omitempty"`
	File         string `json:"file"`
	Line         int    `json:"line"`
	Severity     string `json:"severity"`
	Comment      string `json:"comment"`
	SuggestedFix string `json:"suggested_fix,omitempty"`
	By           string `json:"by,omitempty"`
}

// Enabled 是否已配置上报端点。
func (r *Reporter) Enabled() bool { return r != nil && r.URL != "" }

// BuildPayload 从审查输出组装上报契约。
// findings 取合并且带规范指纹的条目（Merger 写定 FP；无 FP 的旧数据照报，对端兜底）。
// 意见超 MaxFindings 截断（保序，前面的优先）。
func BuildPayload(taskID string, pr *plugin.PRContext, findings []domain.Finding,
	verdict string, plugins []string, durationMS int64, tokenUsage int, occurredAt time.Time) *Payload {
	p := &Payload{
		ArgusTaskID: taskID,
		Platform:    pr.Platform, Owner: pr.Owner, Repo: pr.Repo, Number: pr.Number,
		Title: pr.Title, HeadSHA: pr.HeadSHA, BaseBranch: pr.BaseBranch,
		Verdict: verdict, Plugins: plugins,
		DurationMS: durationMS, TokenUsage: tokenUsage, OccurredAt: occurredAt,
		Findings: make([]Finding, 0, len(findings)),
	}
	for i, f := range findings {
		if i >= MaxFindings {
			break
		}
		p.Findings = append(p.Findings, Finding{
			FP: f.FP, File: f.File, Line: f.Line, Severity: f.Severity,
			Comment: f.Comment, SuggestedFix: f.SuggestedFix, By: f.By,
		})
	}
	return p
}

// Report 上报（带 10s 预算 + 网络错/5xx/429 各 1 次重试；错误仅记日志）。
// ctx 租户解析路由（#97）——调用侧传任务 ctx（runner 已注入 task.TenantID）。
func (r *Reporter) Report(ctx context.Context, payload *Payload) {
	if !r.Enabled() {
		return
	}
	url, token := r.URL, r.Token
	if tid := tenant.FromContext(ctx); len(r.Routes) > 0 {
		if rt, ok := r.Routes[tid]; ok && rt.URL != "" {
			url, token = rt.URL, rt.Token
		}
	}
	async.GoSafe(func() { r.report(payload, url, token) }) // GoSafe：panic 转留痕不进程死（v5.4.2）
}

// report 单次上报执行体。v3.76.0 收敛 agentkit httpx（ctx 感知/限容读体/状态码
// 检查/UTF-8 安全错误摘要单源）：旧手写 post 不读不限响应体、4xx 也盲目重试、
// 每次尝试独立 10s 超时（最坏 ~20s）；现在总预算 10s，4xx 参数错不重试，
// 成功要求对端返回合法 JSON（review-service 承接端恒回 JSON 对象，契约内）。
func (r *Reporter) report(payload *Payload, url, token string) {
	body, err := json.Marshal(payload)
	if err != nil {
		r.logWarn("序列化失败", err)
		return
	}
	var ack json.RawMessage // 只验合法性不取值；对端 Resp 形态演进不耦合本包
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	// sink URL 来自部署方配置（ARGUS_SINK_URL，运维信任源）→ 可信档：
	// 允许内网/回环承接端点，元数据/链路本地地址仍被拨号守卫拒绝。
	err = httpjson.DoJSONWithRetry(ctx, r.httpClient(),
		func() (httpjson.Request, error) {
			return httpjson.Request{
				Method: http.MethodPost,
				URL:    url,
				Body:   body,
				Header: func(h http.Header) {
					h.Set("Content-Type", "application/json")
					if r.Token != "" {
						h.Set("Authorization", "Bearer "+token)
					}
				},
			}, nil
		},
		httpjson.RetryConfig{MaxAttempts: 2, BaseDelay: 500 * time.Millisecond},
		&ack)
	if err != nil {
		r.logWarn("上报失败（重试耗尽）", err)
	}
}

// newHTTPClient 构造单次上报使用的客户端：可信档出站守卫（承接端点允许
// 内网/回环，元数据/链路本地/unspecified 在任何模式下都被逐跳判据与拨号
// 守卫拒绝）。每次调用都读取当前 outbound 设置——见 Reporter.httpClient。
func newHTTPClient() *http.Client {
	return outbound.TrustedClient(http.DefaultClient)
}

func (r *Reporter) logWarn(msg string, err error) {
	if r.Log != nil {
		r.Log.Warn("argus.sink.report_failed", "reason", msg, "error", err.Error())
	}
}
