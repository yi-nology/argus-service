// Package domain 是 Argus 的领域模型叶子包（design §3 分层收口）。
// 只承载跨层共享的纯数据与纯函数：审查意见身份（Finding/指纹）、指纹状态
// （PrintState）与持久化契约（PrintStore/PatternStore）。零内部依赖（除
// learning 只读投影），不 import plugin/storage——storage 与 plugin 都依赖
// 本包，从而解除 storage→plugin 的分层倒置（此前持久化层为拿到 Finding/
// Fingerprint 被迫拖入插件运行时）。
package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"

	akreportutil "github.com/oagentkit/textx/reportutil"
)

// severity 词表钉死为 {high, medium, low}（design §3.3）。
// 实现整体委托 oagentkit/textx/reportutil——单一来源，本包只做 API 门面。
const (
	SeverityCritical = akreportutil.Critical // v5.5.0 四档词表（textx#20）
	SeverityHigh     = akreportutil.High
	SeverityMedium   = akreportutil.Medium
	SeverityLow      = akreportutil.Low
)

// NormalizeSeverity 把任意写法归一到词表。
// 第二个返回值=false 表示原值越界（已归为 low，调用方应记录告警）。
func NormalizeSeverity(s string) (string, bool) {
	return akreportutil.Normalize(s)
}

// SeverityRank 排序权重（high > medium > low）。
func SeverityRank(s string) int { return akreportutil.Rank(s) }

// HigherSeverity 返回两者中更严重的级别。
func HigherSeverity(a, b string) string { return akreportutil.Higher(a, b) }

// EvidenceStatus 证据核验四态（Phase 2 Evidence v2，spec §3）：
// invalid=引用根本不可用（文件不存在/解析失败）；unverified=有引用但未证实
// （行号越界/不在改动集/引用行为空行或注释行/内容对账失败）或未提供结构化
// 证据（纯文字/空）；structurally_valid=文件+行号结构全对但版本或内容未对齐
// （语义未验）；verified=结构对 + 引用行为有效代码行 + 检出版本与 task head
// 对齐 + **内容对账通过**（Finding.EvidenceSnippet/EvidenceSHA256 与引用行
// 归一内容一致，v7.5 #68 语义升级：verified 不再只等于「引用可定位」）。
// 聚合取最差（一 ref invalid 即 invalid）。降级规则：invalid|unverified 的
// critical/high→medium 退出 R5 否决面；structurally_valid/verified 放行。
type EvidenceStatus string

const (
	EvidenceInvalid         EvidenceStatus = "invalid"
	EvidenceUnverified      EvidenceStatus = "unverified"
	EvidenceStructuralValid EvidenceStatus = "structurally_valid"
	EvidenceVerified        EvidenceStatus = "verified"
)

// EvidenceStatusRank 四态序（大=更可信）：聚合取最差=取 rank 最小。
func EvidenceStatusRank(st EvidenceStatus) int {
	switch st {
	case EvidenceVerified:
		return 3
	case EvidenceStructuralValid:
		return 2
	case EvidenceUnverified:
		return 1
	case EvidenceInvalid:
		return 0
	}
	return 1 // 未知态按未证实保守处理
}

// Finding 单条审查意见。Severity 必须经过 NormalizeSeverity 归一化。
type Finding struct {
	File         string `json:"file"`
	Line         int    `json:"line"`
	Severity     string `json:"severity"`
	Comment      string `json:"comment"`
	By           string `json:"by"`
	SuggestedFix string `json:"suggested_fix,omitempty"` // 可选：修复建议代码片段
	// FP 是该条目的入库指纹（prints 表身份，Fingerprint(file, 原始comment, line)），
	// Merger 入库时写定、只读贯穿：渲染层短码与人工反馈（suppress/restore）都必须
	// 用它，禁止拿标注/R3.5 修订后的终稿文本重算——那会让短码与存储指纹漂移，
	// 反馈闭环对"被标注过"的条目全部失效（v3.8.0 修复）。零值 = 未过 merger。
	FP string `json:"fp,omitempty"`
	// Samples best-of-N 采样复现计数 "k/n"（Dispatcher 聚合写定；reviewN<=1 恒空）。
	// 结构化字段而非 Comment 后缀：指纹/prints 原文/模式键由评语派生，标注文本
	// 入键会让跨轮身份漂移（v3.9.1 错账同类教训）。渲染层经 SamplesAnnotation 展示。
	Samples string `json:"samples,omitempty"`
	// Confidence 模型自评信心（0-1，可选；0 = 未提供）。渲染层对低信心条目
	// 加警示标注，供审阅者权重参考——不进指纹（指纹身份由 File+原始 comment+
	// Line 派生，置信度是渲染属性不是身份属性）。
	Confidence float64 `json:"confidence,omitempty"`
	// Verified 沙箱验证环（v3.74.0）：suggested_fix 已在隔离包装命令下应用
	// 并通过编译/测试。渲染属性不入指纹；仅 runner 侧 fixverifier 写定。
	Verified bool `json:"verified,omitempty"`
	// Chain 证据链叙述（v6.10.0 报告契约模板，仅 analyst 模板由模型补充）：
	// 改动侧是什么（file:line）/消费侧哪里依赖旧语义/不改的后果——对外语义、
	// 上报口径、协议变更类问题的决定性证据。渲染属性不进指纹（与 Confidence/
	// Samples 同类：指纹身份=File+原始 comment+Line，渲染标注入键会让跨轮身份
	// 漂移，v3.8.0/v3.9.1 教训）。
	Chain string `json:"chain,omitempty"`
	// Evidence 代码证据引用（v6.18.0 Finding 级证据，v6.17.0 Claim 级同族）：
	// 自由文本 file:line 引用（可含叙述，如"改动侧 a.go:12 与消费侧 b.go:34 不一致"），
	// 模型经 findings JSON evidence 字段产出；交叉校验见 plugin.VerifyFindingEvidence
	//（改动集/触达集内）。校验标注（⚠️ 证据待验证）追加在本字段而非 Comment——
	// 渲染属性不入指纹，与 Chain/Confidence/Samples 同纪律（指纹身份恒为
	// File+原始 comment+Line）。
	Evidence string `json:"evidence,omitempty"`
	// EvidenceSnippet 证据内容对账（v7.5 #68）：引用行的一段预期原文，模型
	// 产出 evidence 时照抄（不做改写）。服务端做空白归一（连续空白折叠为单个
	// 空格）后的包含比对——对不上即 unverified「内容与引用行不符」，不再按
	// verified 放行。可选；与 EvidenceSHA256 二选一或同时给出。
	EvidenceSnippet string `json:"evidence_snippet,omitempty"`
	// EvidenceSHA256 证据行摘要对账（v7.5 #68）：引用行归一内容（连续空白
	// 折叠为单空格）的 SHA-256 十六进制（可带 `sha256:` 前缀，大小写不敏感）。
	// 给自动化上游用（模型算不了哈希）；与 EvidenceSnippet 同时给出时两者
	// 必须都通过。没有 snippet/hash 的证据无法完成内容对账，verified 不可达
	// （上限 structurally_valid）。
	EvidenceSHA256 string `json:"evidence_sha256,omitempty"`
	// EvidenceStatus 证据核验四态（Phase 2 Evidence v2）：由
	// plugin.VerifyFindingsEvidence 在 finish 步骤回写；模型不经 wire 产出本字段
	//（服务端核验结果）。渲染属性不入指纹，与 Evidence/Chain/Confidence 同纪律。
	EvidenceStatus EvidenceStatus `json:"evidence_status,omitempty"`
}

// PrintState 既有指纹状态。
type PrintState struct {
	Status      string // open | suppressed
	Line        int
	Severity    string
	TimesSeen   int
	RegretCount int // 历史被 R3.5 反思剔除次数（误报历史）
	// Comment 入库原文（任何展示标注之前）——模式记账键（PatternKey 由评语
	// 派生）的权威来源；下游 regret 记账用它对齐 PatternPosted 的键。
	Comment string
}

// Fingerprint 意见指纹 v2（file + line + 规范化 comment 的 SHA256 前 16 位）。
// v3.7.0 起行号入指纹：同文件两处不同位置的同模板意见不再共享跨轮统计
// （轮内误并已由 merger 行号相容闸门拦截）。措辞漂移的跨轮匹配由 storage 的
// 近重复匹配层 + 别名表负责（fingerprint v2 消费方必须容忍"精确未命中→近重
// 复并入"语义，勿自行绕过 UpsertPrint）。
// 注意：与 agentkit reportutil.Fingerprint（v1，无行号）不同格式——存量行由
// storage 启动迁移重算（见 storage.migratePrintFingerprintsV2），勿混用两版。
func Fingerprint(file, comment string, line int) string {
	if line < 0 {
		line = 0
	}
	sum := sha256.Sum256([]byte("v2\x1f" + file + "\x1f" + strconv.Itoa(line) + "\x1f" + akreportutil.NormalizeComment(comment)))
	return hex.EncodeToString(sum[:16])
}

// FingerprintNorm 一次规范化同时给出指纹与规范化评语。
// best-of-N 聚簇等需要两者的调用方避免对同一评语跑两遍 NormalizeComment。
func FingerprintNorm(file, comment string, line int) (fp, norm string) {
	if line < 0 {
		line = 0
	}
	norm = akreportutil.NormalizeComment(comment)
	sum := sha256.Sum256([]byte("v2\x1f" + file + "\x1f" + strconv.Itoa(line) + "\x1f" + norm))
	return hex.EncodeToString(sum[:16]), norm
}
