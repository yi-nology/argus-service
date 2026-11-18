// Package notify L1 扩展：审查终态 IM 推送（v3.1，经 octopus 消息平台）。
// HTTP 层曾于 v3.81.0 下沉 ekit/octopus，v3.148.0 回归本仓子包（X-User-Id 机器账号纪律单源）；
// 本包保留任务终态文案渲染与指标记账。未配置 ARGUS_OCTOPUS_URL 时禁用（零调用）。
package notify

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/oagentkit/textx/textutil"
	"github.com/oekit/logx"

	"github.com/yi-nology/argus/internal/notify/octopus"
	"github.com/yi-nology/argus/internal/outbound"
	"github.com/yi-nology/argus/internal/storage"
	"github.com/yi-nology/argus/internal/tenant"

	"github.com/yi-nology/argus/internal/obs"
)

// TenantRoute 单租户 octopus 端点覆盖（#97）。
type TenantRoute struct {
	BaseURL   string
	ChannelID int64
	User      string
	Token     string
}

// Notifier octopus 推送器（nil 安全：nil 接收者直接 no-op）。
type Notifier struct {
	BaseURL   string // 如 http://octopus.svc（不带尾斜杠）
	ChannelID int64  // octopus 渠道 ID（企微/飞书/钉钉机器人渠道）
	User      string // 机器账号名（默认 argus-bot）
	Token     string // 可选 Bearer（octopus 前置网关需要时用）
	Log       logx.Logger
	// Routes 租户路由表（#97）：按 ctx 租户解析端点；nil = 全局单端点形态。
	Routes map[string]TenantRoute

	once   sync.Once
	client *octopus.Client
	// tenantClients 租户客户端缓存（路由命中时按租户懒建；并发安全）。
	mu            sync.Mutex
	tenantClients map[string]*octopus.Client
}

// TaskEvent 终态摘要（runner 装配）。
type TaskEvent struct {
	TaskID     string
	Platform   string
	Owner      string
	Repo       string
	Number     string
	Status     string // done / failed / stale
	Verdict    string
	Rule       string
	Findings   int
	Tokens     int
	Elapsed    time.Duration
	FailReason string
}

// NotifyTerminal 任务终态推送。done/stale 发结论卡，failed 发告警。
func (n *Notifier) NotifyTerminal(ctx context.Context, e TaskEvent) {
	if n == nil || n.BaseURL == "" || n.Log == nil {
		return
	}
	content := renderMarkdown(e)
	if content == "" {
		return
	}
	n.send(ctx, "argus.notify.send", e.TaskID, content)
}

// NotifyRaw 通用 markdown 推送（G5 健康报告等周期性内容；空内容不推送）。
func (n *Notifier) NotifyRaw(ctx context.Context, content string) {
	if n == nil || n.BaseURL == "" || n.Log == nil || strings.TrimSpace(content) == "" {
		return
	}
	n.send(ctx, "argus.notify.raw", "", content)
}

// send 推送执行体（HTTP 层 internal/notify/octopus；状态码/错误在此记账）。
// 租户路由（#97）：ctx 租户在 Routes 命中时按租户端点/客户端发送。
func (n *Notifier) send(ctx context.Context, logKey, taskID, content string) {
	// clientFor 两路都返回非 nil（octopus.New 恒出客户端；BaseURL 空由
	// NotifyTerminal/NotifyRaw 前置判空拦住）——无 nil 守卫面。
	client := n.clientFor(ctx)
	code, err := client.SendMarkdown(ctx, content)
	switch {
	case err != nil:
		obs.IncrNotifyFailure("error")
		n.logWarn(logKey+"_failed", taskID, err.Error())
	case code < 200 || code > 299:
		obs.IncrNotifyFailure(fmt.Sprintf("%d", code))
		n.logWarn(logKey+"_rejected", taskID, fmt.Sprintf("%d", code))
	}
}

// clientFor 解析本次推送的 octopus 客户端：租户路由命中→租户客户端（懒建
// 缓存）；否则全局默认客户端（懒建一次）。BaseURL 未配置=nil（调用方已判空）。
func (n *Notifier) clientFor(ctx context.Context) *octopus.Client {
	tid := tenant.FromContext(ctx)
	if tid != tenant.Default && len(n.Routes) > 0 {
		if rt, ok := n.Routes[tid]; ok && rt.BaseURL != "" {
			n.mu.Lock()
			defer n.mu.Unlock()
			if n.tenantClients == nil {
				n.tenantClients = map[string]*octopus.Client{}
			}
			if c, ok := n.tenantClients[tid]; ok {
				return c
			}
			hc := outbound.TrustedClient(&http.Client{Timeout: 10 * time.Second})
			user := rt.User
			if user == "" {
				user = "argus-bot"
			}
			c := octopus.New(rt.BaseURL, rt.ChannelID, user, rt.Token, 10*time.Second, hc)
			n.tenantClients[tid] = c
			return c
		}
	}
	n.once.Do(func() {
		// 可信档出站守卫：octopus 是配置/运维地址，允许内网，但元数据/
		// 链路本地永远拒绝；octopus 包保持零 internal 依赖（由本包注入 client）。
		hc := outbound.TrustedClient(&http.Client{Timeout: 10 * time.Second})
		n.client = octopus.New(n.BaseURL, n.ChannelID, n.machineUser(), n.Token, 10*time.Second, hc)
	})
	return n.client
}

func (n *Notifier) logWarn(msg, taskID, detail string) {
	if taskID != "" {
		n.Log.Warn(msg, "task", taskID, "error", detail)
		return
	}
	n.Log.Warn(msg, "detail", detail)
}

func (n *Notifier) machineUser() string {
	if n.User != "" {
		return n.User
	}
	return "argus-bot"
}

// renderMarkdown 结论卡（octopus markdown 渠道）。
func renderMarkdown(e TaskEvent) string {
	pr := fmt.Sprintf("%s/%s#%s", e.Owner, e.Repo, e.Number)
	switch e.Status {
	case storage.StatusDone, storage.StatusStale: // 任务状态词表：owner.Const 引用（手抄即被 word-table 门禁钉）
		return fmt.Sprintf("**Argus 审查完成** %s\n- 结论: `%s`（%s）\n- findings: %d\n- 用时: %s / tokens: %d\n- task: %s",
			pr, e.Verdict, e.Rule, e.Findings, e.Elapsed.Round(time.Second), e.Tokens, e.TaskID)
	case storage.StatusFailed:
		// v3.8.0：FailReason 可能含上游错误串（端点/URL 细节），IM 推送截断防泄露长串
		//（v3.82.0 收敛 textutil.TruncEllipsis，消除 []rune 全量拷贝）
		reason := textutil.TruncEllipsis(e.FailReason, 300)
		return fmt.Sprintf("**Argus 审查失败** %s\n- 原因: %s\n- task: %s", pr, reason, e.TaskID)
	default:
		return "" // pending/running 不推送
	}
}
