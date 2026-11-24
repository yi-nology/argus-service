// Package reposettings 服务侧按仓配置（v3.187.0）：部署方经 admin API 写入的
// 仓库级覆盖（键 platform/owner/repo，值 JSON 落 repo_settings 表）。与仓库内
// .reviewagents.yaml（半可信、只能收窄）分层：本包是 admin token 写入的可信面，
// 覆盖值可直接上调或下调部署默认（预算/上限类旋钮），不碰供应链红线（SAST/
// OCR/LLM endpoint 永远只来自部署 env）。
package reposettings

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// maxKnobValue 旋钮防呆上限（int 溢出防线；1e9 token/字符在真实部署下不可能触达）。
const maxKnobValue = 1_000_000_000

// Settings 按仓覆盖集。指针字段区分「未设置」（落全局默认）与显式值；0 与负数
// 在 Parse 拒绝（预算/上限旋钮取 0 无意义，删除覆盖走 DELETE API 而非写 0）。
type Settings struct {
	// MonthlyTokenBudget 覆盖全局 ARGUS_REPO_MONTHLY_TOKEN_BUDGET（仓库月度
	// token 预算闸；runner budgetGate 消费，#32 起含预留账本判定）。
	MonthlyTokenBudget *int `json:"monthly_token_budget,omitempty"`
	// TaskTokenBudget 覆盖 EffectiveTaskTokenBudget()（单任务 token 预算，
	// runner Execute 消费——显式值绕过窗口公式派生）。
	TaskTokenBudget *int `json:"task_token_budget,omitempty"`
	// MaxDiffChars 覆盖 MaxDiffCharsResolved()（diff 总量上限；runner
	// prepareContext 与 service describe 共用）。
	MaxDiffChars *int `json:"max_diff_chars,omitempty"`
	// ReportTemplate 覆盖报告契约模板服务默认（v6.11.0；runner 模板裁决链
	// admin 层：任务级 > 本层 > .reviewagents.yaml > ARGUS_REPORT_TEMPLATE）。
	// 词表校验（standard|analyst|minimal）不在本包——reposettings 刻意零
	// internal 依赖（L0），消费端 runner 用 reporttmpl.IsValid 复核，非法值
	// 告警回退下一层（与 Resolve 防御语义一致）。形状校验在 Parse。
	ReportTemplate *string `json:"report_template,omitempty"`

	// ---- 回复控制（v8.4.0 仓级「控制回复」） ----
	// CommentThrottleMinutes 回复频次：同一 PR 总结评论最小间隔分钟。窗口内
	// 新报告走「更新既有评论」而非新发（防评论刷屏）；未设置=不节流。
	CommentThrottleMinutes *int `json:"comment_throttle_minutes,omitempty"`
	// FindingsCap 回复数量：单报告 findings 上限（severity 降序取 top N，
	// 溢出以「另 N 条低优略去」计数行呈现）；未设置=不封顶。
	FindingsCap *int `json:"findings_cap,omitempty"`
	// SeverityFloor 回复级别：地板以下 findings 静默（报告只留计数）。
	// 词表 critical|high|medium|low（SeverityFloorVocab 单源）；未设置=全报。
	SeverityFloor *string `json:"severity_floor,omitempty"`
	// OCRDelegateEnabled OCR 按仓开关（AND 部署 ARGUS_OCR_BIN——endpoint
	// 红线不动，本开关只做「用不用」）。false=该仓跳过 OCR delegate。
	OCRDelegateEnabled *bool `json:"ocr_delegate_enabled,omitempty"`
}

// Parse 解析并校验按仓配置 JSON。DisallowUnknownFields：拼写错误（如
// maxDiffChars）直接报错而不是静默无效——配置写错了比不生效更危险；
// 尾随内容一并拒绝（防手滑拼接两段 JSON 只生效第一段的误读）。
func Parse(data []byte) (*Settings, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var s Settings
	if err := dec.Decode(&s); err != nil {
		return nil, fmt.Errorf("解析 repo_settings: %w", err)
	}
	var extra json.RawMessage
	if err := dec.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("repo_settings 含尾随内容（只允许单个 JSON 对象）")
	}
	for name, v := range map[string]*int{
		"monthly_token_budget":     s.MonthlyTokenBudget,
		"task_token_budget":        s.TaskTokenBudget,
		"max_diff_chars":           s.MaxDiffChars,
		"comment_throttle_minutes": s.CommentThrottleMinutes,
		"findings_cap":             s.FindingsCap,
	} {
		if v == nil {
			continue
		}
		limit := maxKnobValue
		switch name {
		case "comment_throttle_minutes":
			limit = maxCommentThrottleMin
		case "findings_cap":
			limit = maxFindingsCap
		}
		if *v <= 0 || *v > limit {
			return nil, fmt.Errorf("%s 须为 1~%d 的正整数，得到 %d", name, limit, *v)
		}
	}
	// severity_floor 词表校验（前后端契约单源：写错档位比不生效更危险）。
	if s.SeverityFloor != nil {
		ok := false
		for _, w := range severityFloorVocab {
			if *s.SeverityFloor == w {
				ok = true
				break
			}
		}
		if !ok {
			return nil, fmt.Errorf("severity_floor 非法（词表 %v）: %q", severityFloorVocab, *s.SeverityFloor)
		}
	}
	// report_template 形状校验（词表归消费端 reporttmpl.IsValid）：非空、
	// ≤32 字符、不含空白/控制字符——配置写错比不生效更危险，形状层先挡。
	if s.ReportTemplate != nil {
		v := *s.ReportTemplate
		if v != strings.TrimSpace(v) || v == "" || len(v) > 32 || strings.ContainsAny(v, "\t\n\r \x00") {
			return nil, fmt.Errorf("report_template 形状非法（须为非空白 ≤32 字符单 token）: %q", v)
		}
	}
	return &s, nil
}

// Normalize 规范形态落库（omitempty 收敛后 marshal；读取侧 Parse 单源）。
func (s *Settings) Normalize() ([]byte, error) { return json.Marshal(s) }

// IsZero 全部字段未设置（调用方应拒收——空覆盖无意义，语义等同 DELETE）。
func (s *Settings) IsZero() bool {
	return s == nil || (s.MonthlyTokenBudget == nil && s.TaskTokenBudget == nil &&
		s.MaxDiffChars == nil && s.ReportTemplate == nil &&
		s.CommentThrottleMinutes == nil && s.FindingsCap == nil &&
		s.SeverityFloor == nil && s.OCRDelegateEnabled == nil)
}

// MonthlyTokenBudgetOr 生效值：未设置落 def（调用方传全局缺省）。nil receiver
// 安全（nil Settings = 全默认）。
func (s *Settings) MonthlyTokenBudgetOr(def int) int {
	if s == nil {
		return def
	}
	return or(s.MonthlyTokenBudget, def)
}

// TaskTokenBudgetOr 生效值：未设置落 def。
func (s *Settings) TaskTokenBudgetOr(def int) int {
	if s == nil {
		return def
	}
	return or(s.TaskTokenBudget, def)
}

// MaxDiffCharsOr 生效值：未设置落 def。
func (s *Settings) MaxDiffCharsOr(def int) int {
	if s == nil {
		return def
	}
	return or(s.MaxDiffChars, def)
}

// ReportTemplateOr 生效值：未设置落 def（空串 = 未设置，调用方再进 reporttmpl
// 裁决链）。nil receiver 安全。
func (s *Settings) ReportTemplateOr(def string) string {
	if s == nil {
		return def
	}
	if s.ReportTemplate == nil {
		return def
	}
	return *s.ReportTemplate
}

// CommentThrottleMinutesOr 生效值：未设置落 def（0 = 不节流）。
func (s *Settings) CommentThrottleMinutesOr(def int) int {
	if s == nil {
		return def
	}
	return or(s.CommentThrottleMinutes, def)
}

// FindingsCapOr 生效值：未设置落 def（0 = 不封顶）。
func (s *Settings) FindingsCapOr(def int) int {
	if s == nil {
		return def
	}
	return or(s.FindingsCap, def)
}

// SeverityFloorOr 生效值：未设置落 def（空串 = 全报）。
func (s *Settings) SeverityFloorOr(def string) string {
	if s == nil || s.SeverityFloor == nil {
		return def
	}
	return *s.SeverityFloor
}

// OCRDelegateEnabledOr 生效值：未设置落 def（true = 跟随部署态）。
func (s *Settings) OCRDelegateEnabledOr(def bool) bool {
	if s == nil || s.OCRDelegateEnabled == nil {
		return def
	}
	return *s.OCRDelegateEnabled
}

func or(p *int, def int) int {
	if p == nil {
		return def
	}
	return *p
}

// ---------- 回复控制（v8.4.0 仓级「控制回复」三旋钮 + OCR 按仓开关） ----------

// 回复控制形状常量（Parse 校验与前端提示同源）。
const (
	// maxCommentThrottleMin 频次节流上限：24h——更长间隔等于「别再回了」，用
	// 删除评论/关 webhook 表达，不作为节流语义。
	maxCommentThrottleMin = 1440
	// maxFindingsCap 单报告 findings 上限上限（防呆；真实审查 50 条/报告已
	// 超出评审人注意力预算）。
	maxFindingsCap = 50
)

// 级别地板词表（severity 升序；地板以下 findings 静默只留计数）。
// 与 domain.Finding.Severity 词表对齐（词表门禁 harness-word-table 已钉，
// 此处为导出消费面——前后端契约单源）。
var severityFloorVocab = []string{"critical", "high", "medium", "low"}

// SeverityFloorVocab 导出地板词表（前端 Segmented 选项与校验同源）。
func SeverityFloorVocab() []string { return severityFloorVocab }
