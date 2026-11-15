package plugin

import (
	"context"
	"time"
)

// AgentManifest 插件清单（GET /agents 可观测，含 needs/triggers）。
type AgentManifest struct {
	Name     string
	Version  string
	Kind     Kind
	Protocol Protocol // kind=remote 时有效
	Skills   []string
	Needs    Needs
	Triggers Trigger
	Cost     string
	Timeout  time.Duration
	// ReviewN best-of-N 同质采样次数（plugins.yaml reviewN；0/1 = 单次）。
	// >1 时 Dispatcher 对同一插件并发跑 N 次（钳制 ≤ MaxReviewN）并聚合：
	// 复现 ≥2 的发现升一级严重度并标注 k/n（bestofn.go）。高危路径 opt-in。
	ReviewN int
}

// AgentPlugin L4 统一契约：所有形态（builtin/cli/remote）都收敛到这两个方法。
type AgentPlugin interface {
	Manifest() AgentManifest
	Invoke(ctx context.Context, task Task) (Result, error)
}

// PRContext PR 轨迹上下文（由 runner 从平台 API 组装）。
type PRContext struct {
	Platform      string
	Owner         string
	Repo          string
	Number        string
	Title         string
	Body          string // PR 描述（攻击者可控，进提示词前须包数据区）
	Author        string
	Labels        []string
	HeadSHA       string
	BaseBranch    string
	HeadBranch    string
	DefaultBranch string
	// EventType 触发本任务的平台事件（cr.opened/cr.synchronize/...；CLI 手动任务为空）。
	// 供 Triggers.Events 匹配；空值视为不限制。
	EventType string
	// CIStatus head SHA 的合并 CI 状态（v6.11.0；统一词表 pending|running|success|
	// failure|error|canceled，空 = 未取到/平台无能力——渲染层省略该段）。best-effort
	// 获取（capability 门 + 超时 + 静默降级），不参与任何裁决。
	CIStatus string
}

// Key 返回 PR 唯一键（storage/staleness 共用）。
func (p PRContext) Key() (platform, owner, repo, number string) {
	return p.Platform, p.Owner, p.Repo, p.Number
}

// DiffFile 单文件变更摘要（diffprep 清洗后）。
type DiffFile struct {
	Path      string
	Additions int
	Deletions int
	IsBinary  bool
	IsNew     bool
	IsDeleted bool
	IsRenamed bool
	Included  bool // 是否进入了 Text（噪声文件被剔除但保留元信息）
	// AlreadyReviewed 增量审查标记：该文件 diff 与上次审查完全一致，无需重新审查。
	AlreadyReviewed bool
	// RepoExcluded 仓库级排除标记（v3.15.0 .reviewagents.yaml review.exclude/include_paths）：
	// 用户主动排除，与预算截断无关——不进截断清单，不计入 Truncated。
	RepoExcluded bool
}

// DiffContext 清洗后的 diff（design §4 术语：diffprep）。
type DiffContext struct {
	Text      string
	Files     []DiffFile
	Truncated bool
	// NoNewChanges 增量审查标记：当前 diff 与上次审查完全一致，无新变更。
	NoNewChanges bool
}

// AcceptanceCriterion 验收条目。
type AcceptanceCriterion struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

// RequirementSpec R1 产出：验收清单 + 模糊点（改写不了的原样进 Ambiguous，禁止编造）。
type RequirementSpec struct {
	Source    string                `json:"source"` // issue #N | PR 描述
	IssueRefs []string              `json:"issue_refs,omitempty"`
	ACs       []AcceptanceCriterion `json:"acceptance_criteria"`
	Ambiguous []string              `json:"ambiguous,omitempty"`
}

// 完成度词表（R3，钉死）。
const (
	ACImplemented  = "implemented"
	ACPartial      = "partial"
	ACMissing      = "missing"
	ACUnverifiable = "unverifiable"
)

// ACCheck 单条 AC 的完成度判定。
type ACCheck struct {
	ID       string `json:"id"`
	Status   string `json:"status"`             // implemented|partial|missing|unverifiable
	Evidence string `json:"evidence,omitempty"` // file:line（implemented/partial 必须携带，校验器强制）
	Detail   string `json:"detail,omitempty"`
}

// ScopeCreepItem 需求外改动。
type ScopeCreepItem struct {
	File string `json:"file"`
	Note string `json:"note"`
}

// ComplianceReport R3 产出。
type ComplianceReport struct {
	Checks     []ACCheck        `json:"checks"`
	ScopeCreep []ScopeCreepItem `json:"scope_creep,omitempty"`
	Summary    string           `json:"summary,omitempty"`
}
