// Package adapter_remote remote 形态适配器（design §3.2/§3.5）。
// v3.1 实装协议子集：mcp（mark3labs/mcp-go streamable http client）与
// a2a（Agent Card 发现 → message/send → tasks/get 轮询 artifact）。
// 返回契约与 builtin/cli 同一 findings schema（{"findings":[{file,line,severity,
// comment}...],"notes"}）：a2a 发结构化任务 payload 收 findings JSON；mcp 走
// review-service mcp-server 编排契约（create_review 平铺参数同步等 →
// get_review_findings 取明细，见 mcp.go）。
package adapter_remote

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/oagentkit/guardx/logredact"
	"github.com/oagentkit/textx/textutil"
	"github.com/oagentkit/tools/workcopy"
	"github.com/oekit/logx"

	"github.com/yi-nology/argus/internal/plugin"
)

// log 包级留痕（与 ocrshim 同形态 Delegating）：pre-unshallow 等 best-effort
// 面失败只留痕不阻断，事件键前缀 remote.*。
var log = logx.Delegating("adapter_remote")

// payloadBodyMaxChars remote payload 里 PR 描述的截断上限。
// diff 不在此截断（diffprep 已按模型窗口清洗），描述截断只防恶意刷。
const payloadBodyMaxChars = 24_000

// unshallowTimeout 交接前历史预补全的独立上限（对齐 ocrshim/ocrdeleg 的 5m
// 形态：大仓库全量历史 fetch 慢，不随插件超时收缩）。
const unshallowTimeout = 5 * time.Minute

// WorkcopyPool remote 插件按需的工作副本能力（*workcopy.Pool 结构性满足）。
// needs:[working-copy] 的 remote 插件（如经 ocrshim 调 OCR）在 Invoke 内
// Ensure/Release——与 cli/acpx 适配器同模式，目录经 payload 下发给远端。
// EnsureMergeBase 交接前的历史预补全（带池子凭证）：远端 sidecar 无凭证通道，
// 私有远端它自己的 unshallow 必 "could not read Username"（tools#160）；
// 预补全后其 EnsureMergeBase 本地探测即过。
type WorkcopyPool interface {
	Ensure(ctx context.Context, key workcopy.WorktreeKey) (string, error)
	Release(key workcopy.WorktreeKey)
	EnsureMergeBase(ctx context.Context, dir string, key workcopy.WorktreeKey, baseBranch string, timeout time.Duration, opts ...workcopy.DeepenOption) error
}

// Remote 远端 agent 插件。endpoint/tool 只在 plugins.yaml 声明（§3.4 红线），
// 仓库 .reviewagents.yaml 仅能启用/禁用/排除（与 cli 插件同路径）。
type Remote struct {
	manifest plugin.AgentManifest
	Endpoint string
	Tool     string // mcp 协议：create_review 工具名（review-service mcp-server 契约；缺省 review）
	// AuthToken Bearer 共享密钥（空 = 不带鉴权头；与对端 auth_token_env 配对，
	// 如 ocrshim 的 OCRSHIM_TOKEN）。非 loopback 的 sidecar endpoint（compose
	// 跨容器）网络隔离不足以限定"只有 argus"，建议配置。
	AuthToken string
	wc        WorkcopyPool
}

// New 按 plugins.yaml 声明构造；protocol 仅接受 a2a|mcp。
// 声明 needs:[working-copy] 时 wc 必须就位（否则启动期报错停用）。
func New(m plugin.AgentManifest, protocol plugin.Protocol, endpoint, tool, authToken string, wc WorkcopyPool) (*Remote, error) {
	m.Kind = plugin.KindRemote
	m.Protocol = protocol
	if m.Timeout <= 0 {
		m.Timeout = 180 * time.Second
	}
	if endpoint == "" {
		return nil, fmt.Errorf("remote 插件 %s 缺少 endpoint", m.Name)
	}
	if tool == "" {
		tool = "review"
	}
	if m.Needs.WorkingCopy && wc == nil {
		return nil, fmt.Errorf("remote 插件 %s 声明 working-copy 但未接入工作副本服务", m.Name)
	}
	return &Remote{manifest: m, Endpoint: endpoint, Tool: tool, AuthToken: authToken, wc: wc}, nil
}

func (r *Remote) Manifest() plugin.AgentManifest { return r.manifest }

// Invoke 按协议分发。远端失败返回错误即触发 Merger 部分失败容忍（降级注明）。
// needs:[working-copy] 时先准备工作副本（浅克隆 + checkout PR head），
// 目录经 payload.workcopy_dir 下发，Invoke 结束释放。
func (r *Remote) Invoke(ctx context.Context, task plugin.Task) (plugin.Result, error) {
	ctx, cancel := context.WithTimeout(ctx, r.manifest.Timeout)
	defer cancel()
	dir := ""
	if r.manifest.Needs.WorkingCopy {
		key := plugin.PRToKey(task.PR)
		var err error
		if dir, err = r.wc.Ensure(ctx, key); err != nil {
			return plugin.Result{}, fmt.Errorf("准备工作副本失败: %w", err)
		}
		defer r.wc.Release(key)
		// 交接前历史预补全（tools#160/#184，池子凭证自动注入）：远端 sidecar
		// （ocrshim）无凭证通道，私有远端它自己的 unshallow 必 "could not read
		// Username" → OCR preview 静默降级。best-effort：失败留给对端 unshallow
		// 兜底（公有/file:// 形态仍可用），不阻断交接。
		if b := reviewBaseBranch(task.PR); b != "" {
			if merr := r.wc.EnsureMergeBase(ctx, dir, key, b, unshallowTimeout); merr != nil {
				// 错误链可能回显带凭证 URL（git stderr）——进日志前脱敏
				log.Warn("remote.workcopy_pre_unshallow_failed",
					"plugin", r.manifest.Name, "error", logredact.Redact(merr.Error()))
			}
		}
	}
	switch r.manifest.Protocol {
	case plugin.ProtocolMCP:
		return r.invokeMCP(ctx, task, dir)
	case plugin.ProtocolA2A:
		return r.invokeA2A(ctx, task, dir)
	default:
		return plugin.Result{}, fmt.Errorf("remote 插件 %s 不支持的协议: %s", r.manifest.Name, r.manifest.Protocol)
	}
}

// remotePayload 发给远端的结构化任务（与 builtin/cli 共享的审查输入语义）。
type remotePayload struct {
	Platform      string   `json:"platform"`
	Owner         string   `json:"owner"`
	Repo          string   `json:"repo"`
	Number        string   `json:"number"`
	Title         string   `json:"title"`
	Body          string   `json:"body,omitempty"`
	Author        string   `json:"author,omitempty"`
	Labels        []string `json:"labels,omitempty"`
	HeadSHA       string   `json:"head_sha,omitempty"`
	BaseBranch    string   `json:"base_branch,omitempty"`
	HeadBranch    string   `json:"head_branch,omitempty"`
	Diff          string   `json:"diff,omitempty"`
	ChangeSummary string   `json:"change_summary,omitempty"`
	// WorkcopyDir 工作副本目录（needs:[working-copy] 时下发；远端 sidecar 在此执行）。
	WorkcopyDir string `json:"workcopy_dir,omitempty"`
}

// reviewBaseBranch 审查基线分支：BaseBranch 缺省回落 DefaultBranch（与
// ocrdeleg 的 inject 基线同口径——push/手动任务无 PR base 时对端仍能定位
// merge-base；payload 与交接预补全共用此单源，避免两处解析漂移）。
func reviewBaseBranch(pr plugin.PRContext) string {
	if b := strings.TrimSpace(pr.BaseBranch); b != "" {
		return b
	}
	return strings.TrimSpace(pr.DefaultBranch)
}

func buildPayload(task plugin.Task, workcopyDir string) (*remotePayload, string, error) {
	p := &remotePayload{
		Platform:      task.PR.Platform,
		Owner:         task.PR.Owner,
		Repo:          task.PR.Repo,
		Number:        task.PR.Number,
		Title:         task.PR.Title,
		Body:          task.PR.Body,
		Author:        task.PR.Author,
		Labels:        task.PR.Labels,
		HeadSHA:       task.PR.HeadSHA,
		BaseBranch:    reviewBaseBranch(task.PR),
		HeadBranch:    task.PR.HeadBranch,
		ChangeSummary: task.Artifacts[plugin.ArtifactChangeSummary],
	}
	p.Body = textutil.TruncNote(p.Body, payloadBodyMaxChars, "截断")
	p.WorkcopyDir = workcopyDir
	if task.Diff != nil {
		p.Diff = task.Diff.Text
	}
	b, err := json.Marshal(p)
	return p, string(b), err
}

// parseFindings 从远端响应文本提取 findings JSON（与 builtin 同 schema）。
func parseFindings(text string) (plugin.Result, error) {
	out, err := plugin.DecodeFindingsJSON(text)
	if err != nil {
		return plugin.Result{}, fmt.Errorf("remote 响应解析失败: %w（原文 %.200s）", err, text)
	}
	for i := range out.Findings {
		if out.Findings[i].By == "" {
			out.Findings[i].By = "remote"
		}
	}
	return plugin.Result{Findings: out.Findings, Notes: out.Notes}, nil
}

// textFromContents 拼接 MCP text content 数组。
func textFromContents(texts []string) string {
	return strings.Join(texts, "\n")
}
