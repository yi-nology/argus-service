// evidence.go —— 证据可信度校验（v6.17.0 方向 A 起，评估报告 2026-11-12 实锤缺口）。
//
// 背景：SanitizeAnalyst 只钳制字段合法性/条数/长度，未验证每个 file:line 证据
// 是否真实成立（评估报告：「严格验证每个 file:line、声明和风险链是否真实成立」）。
// 方向 A 落地：Evidence 证据记录 + 代码级校验（文件在改动集/触达集、行号有效），
// 高风险声明（missing/inconsistent）证据不可验证时降级标注——提高每条意见的
// 可采信程度，而非增加 Reviewer 数量。
// v6.18.0 扩展 domain.Finding 级同族：domain.Finding.Evidence 字段 + VerifyFindingEvidence/
// VerifyFindingsEvidence（高危 critical/high 证据不可验证降级 medium 退出 R5
// 否决面），Claim 级语义不变。
//
// 纪律：
//   - 纯函数（无文件系统 I/O）：校验基于 task.Diff.Files 与 ArtifactCodegraphTouched
//     渲染面交叉——不引新 Artifact 键、不动 runner 注入面；
//   - 软标注不丢弃：证据不可验证的 claim 保留原文，Evidence 追加「⚠️ 证据待验证」
//     ——声明本身可能是真问题，只是证据形态无法确认（不静默丢弃真问题）；
//   - 高风险状态降级：missing/inconsistent（深度评审最高危信号）证据格式解析失败
//     时 Status 降级 partial——状态错误的声明比缺失更有害（NormalizeClaimStatus 同
//     纪律），但降级而非删除（保留线索）。domain.Finding 级同纪律：critical/high → medium。
package plugin

import (
	"errors"
	"regexp"
	"strconv"
	"strings"

	"github.com/yi-nology/argus/internal/domain"
)

// EvidenceRef 从 Claim.Evidence 文本解析的 file:line 代码证据引用。
type EvidenceRef struct {
	File string // 仓库相对路径（改动集内/触达集内可校验）
	Line int    // 行号（1-based；0 = 解析失败）
}

// evidenceRefRe file:line 引用正则：支持 `path/to/file.go:123` 形态（文件路径
// 可含中文/短横/下划线/点，行号 1-6 位数字；冒号后非数字的如 URL 不匹配）。
var evidenceRefRe = regexp.MustCompile(`([\w./-]+\.[A-Za-z0-9]{1,8}:\d{1,6})`)

// ParseEvidenceRefs 从 Claim.Evidence 自由文本解析全部 file:line 引用。
// 自由文本里可含叙述（"改动侧 file.go:12 与消费侧 x.go:34 不一致"）。
// 返回空切片 = 无 file:line 形态引用（可能是纯文字差距说明）。
func ParseEvidenceRefs(s string) []EvidenceRef {
	if s == "" {
		return nil
	}
	matches := evidenceRefRe.FindAllStringSubmatch(s, -1)
	seen := map[string]bool{}
	var refs []EvidenceRef
	for _, m := range matches {
		if len(m) != 2 {
			continue
		}
		ref := parseOneRef(m[1])
		if ref == nil || seen[m[1]] {
			continue
		}
		seen[m[1]] = true
		refs = append(refs, *ref)
	}
	return refs
}

// parseOneRef "path/to/file.go:123" → EvidenceRef。
func parseOneRef(s string) *EvidenceRef {
	idx := strings.LastIndexByte(s, ':')
	if idx <= 0 {
		return nil
	}
	file := s[:idx]
	line, err := strconv.Atoi(s[idx+1:])
	if err != nil || line <= 0 {
		return nil
	}
	return &EvidenceRef{File: file, Line: line}
}

// EvidenceResult 单条 Claim/domain.Finding 的证据校验结论。
// SourceReader 证据核验的文件读取注入面（Phase 2 Evidence v2，spec §3）：
// I/O 边界显式化——核心保持纯函数，实现由装配侧提供（runner 注入 workcopy
// 池读取面）。构造契约：实现绑定**本任务检出版本**（HeadSHA()==task.HeadSHA），
// verify 层判 HeadSHA() 非空只是 verified 的必要条件（v7.5 #68 起还需内容
// 对账：EvidenceSnippet/EvidenceSHA256 与引用行归一内容一致）；nil reader =
// 集合级降级（Claim 级/基础设施不可用路径，上限 structurally_valid）。
type SourceReader interface {
	// ReadLines 读绑定检出下 path 的文件内容行。文件不存在须透传
	// fs.ErrNotExist（errors.Is 可对表）；单文件超出大小/行数预算须透传
	// ErrSourceFileTooLarge（调用方降级 unverified，不判 invalid 冤枉证据）；
	// 其余错误（凭证/网络/围栏/超时）按基础设施失败处理（调用方降级集合级）。
	// 实现应按 path 缓存：同一任务内同名文件只读一次（#69）。
	ReadLines(path string) ([]string, error)
	// HeadSHA 绑定的检出版本（空 = 无版本保证，verified 资格降为 structural）。
	HeadSHA() string
}

var (
	// ErrSourceFileTooLarge 单文件超出读取预算（字节/行数上限，v7.5 #69）：
	// 文件真实存在但不在可核验范围内——调用方必须降级并显式标注，不能
	// 按 fs.ErrNotExist 判 invalid。
	ErrSourceFileTooLarge = errors.New("evidence source file exceeds read budget")
	// ErrSourceBudgetExceeded 任务级读取预算耗尽（引用数/总字节）：
	// 与 ErrSourceFileTooLarge 同口径——降级 unverified 并标注预算截断。
	ErrSourceBudgetExceeded = errors.New("evidence source read budget exhausted")
)

type EvidenceResult struct {
	Refs     []EvidenceRef         // 解析出的引用（空 = 无 file:line）
	Status   domain.EvidenceStatus // Phase 2 四态（finding 级回写源）
	Verified bool                  // Status∈{structurally_valid, verified}（兼容旧消费语义）
	Note     string                // 校验注记（追加到 Evidence 末尾；空=放行不标）
}

// evidenceWarnMark 证据不可验证的软标注锚（幂等判定用：已含此锚不再叠加）。
const evidenceWarnMark = "⚠️ 证据待验证"

// verifyEvidenceText 自由文本证据的引用解析 + 交叉校验核心（Claim/domain.Finding 级
// 共用）：file 在 changed 集或 touched 集内且行号 > 0 才可验证。无 file:line
// 引用时判定为「纯文字差距说明」——不算证据缺陷（Verified=true 且无注记）。
// changed：改动文件清单（Diff.Files 的 Path 列表）；touched：ArtifactCodegraphTouched
// 渲染面里出现过的文件路径（可为空）。
func verifyEvidenceText(s string, changed, touched []string) EvidenceResult {
	refs := ParseEvidenceRefs(s)
	if len(refs) == 0 {
		// 纯文字（或空）：无引用可校验，不算缺陷
		return EvidenceResult{Refs: nil, Verified: true, Note: ""}
	}
	inSet := fileSet(changed, touched)
	verified := true
	var badRefs []EvidenceRef
	for _, ref := range refs {
		if ref.Line > 0 && inSet[ref.File] {
			continue
		}
		verified = false
		badRefs = append(badRefs, ref)
	}
	note := ""
	if !verified {
		var b strings.Builder
		b.WriteString(" " + evidenceWarnMark + "（")
		for i, r := range badRefs {
			if i > 0 {
				b.WriteString("、")
			}
			b.WriteString(r.File)
			b.WriteString(":")
			b.WriteString(strconv.Itoa(r.Line))
		}
		b.WriteString(" 不在改动集/触达集，或行号无效）")
		note = b.String()
	}
	return EvidenceResult{Refs: refs, Verified: verified, Note: note}
}

// VerifyClaimEvidence 校验单条 Claim 的证据。无 file:line 引用时判定为
// 「纯文字差距说明」——不算证据缺陷（missing/inconsistent 的 evidence 可以是
// 差距叙述）。
func VerifyClaimEvidence(c Claim, changed, touched []string) EvidenceResult {
	return verifyEvidenceText(c.Evidence, changed, touched)
}

// VerifyFindingEvidence 校验单条 domain.Finding 的证据（v6.18.0 集合级起步，
// Phase 2 Evidence v2 升四态，v7.5 #68 起 verified 还要求内容对账）：
// r nil=集合级（上限 structural），非 nil 走文件/行号/版本/内容深核验。
// Evidence 可选字段语义保留（不强制必填），但空/纯文字现记 unverified
// 「未提供结构化证据」（Phase 2 翻转：不再叫 verified）。
func VerifyFindingEvidence(f domain.Finding, changed, touched []string, r SourceReader) EvidenceResult {
	return verifyFindingEvidenceBudgeted(f, changed, touched, r, defaultEvidenceBudget())
}

// verifyFindingEvidenceBudgeted 单条核验的内部入口（批量路径共享任务预算）。
func verifyFindingEvidenceBudgeted(f domain.Finding, changed, touched []string, r SourceReader, budget *evidenceBudget) EvidenceResult {
	exp := evidenceContent{Snippet: f.EvidenceSnippet, SHA256: f.EvidenceSHA256}
	st, refs, note := assessEvidenceStatus(f.Evidence, changed, touched, r, exp, budget)
	return EvidenceResult{
		Refs:     refs,
		Status:   st,
		Verified: st == domain.EvidenceStructuralValid || st == domain.EvidenceVerified,
		Note:     note,
	}
}

// fileSet changed + touched 的去重集合。
func fileSet(changed, touched []string) map[string]bool {
	m := make(map[string]bool, len(changed)+len(touched))
	for _, f := range changed {
		if f != "" {
			m[f] = true
		}
	}
	for _, f := range touched {
		if f != "" {
			m[f] = true
		}
	}
	return m
}

// VerifiedAnnotation 渲染标注：校验通过 ✅ / 待验证 ⚠️（Evidence 文本追加用）。
func VerifiedAnnotation(v EvidenceResult) string {
	if len(v.Refs) == 0 {
		return "" // 纯文字不标注
	}
	return v.Note
}

// HighRiskClaimStatus 判定高风险声明状态（missing/inconsistent = 深度评审
// 最高危信号，证据必须可验证；partial/implemented/undeclared 证据缺陷只标注）。
func HighRiskClaimStatus(status string) bool {
	return status == ClaimMissing || status == ClaimInconsistent
}

// VerifyClaimsEvidence 批量校验 sec.Claims 证据并就地更新（Evidence 追加
// ⚠️ 注记；高风险状态证据不可验证时降级 partial）。changed=改动文件清单；
// touched=触达集文件清单（ArtifactCodegraphTouched 提取）。返回校验通过/总数。
func VerifyClaimsEvidence(sec *AnalystSections, changed, touched []string) (verified, total int) {
	if sec == nil {
		return 0, 0
	}
	for i := range sec.Claims {
		c := &sec.Claims[i]
		res := VerifyClaimEvidence(*c, changed, touched)
		total++
		if res.Verified {
			verified++
			continue
		}
		c.Evidence += VerifiedAnnotation(res)
		// 高风险状态证据不可验证 → 降级 partial（保留声明本身，状态不再
		// 断言最高危——渲染层状态图标随之变化，降低误报权重）
		if HighRiskClaimStatus(c.Status) {
			c.Status = ClaimPartial
		}
	}
	return verified, total
}

// HighRiskFindingSeverity 高危严重度判定（v6.18.0）：critical/high = R5 一票
// 否决面（verdict.go R5.2 按 Critical/High 驱动 REQUEST_CHANGES）——与 Claim 级
// HighRiskClaimStatus（missing/inconsistent 断言最高危）同纪律：证据不可验证时
// 必须退出否决面。
func HighRiskFindingSeverity(sev string) bool {
	return sev == domain.SeverityCritical || sev == domain.SeverityHigh
}

// VerifyFindingsEvidence 批量校验 mf.Findings 证据并就地更新（v6.18.0，
// VerifyClaimsEvidence 的 domain.Finding 级同族）：
//   - Evidence 追加 ⚠️ 软标注（幂等：已含标注锚不再叠加，重复调用零变化）；
//   - 高危（critical/high）证据不可验证 → 降级 medium——退出 R5 一票否决面，
//     状态错误的高危比缺失更有害，但降级而非删除（保留线索，medium/low 只标注）；
//   - 空证据/纯文字不判（domain.Finding.Evidence 可选，模型未给引用不算缺陷）。
//
// 返回校验通过/总数（通过=含纯文字与空证据，与 Claim 级口径一致）。
// VerifyFindingsEvidence 批量四态核验（Phase 2 Evidence v2）：回写每条
// Finding.EvidenceStatus；invalid|unverified → ⚠️软标注（幂等）+ 高危降级
// medium 退出 R5 否决面；structurally_valid|verified 放行（不标不降）。
// r nil = 集合级降级路径。verified 计数 = 结构核验通过条数（含两放行态）。
// 整批共享一个任务级预算（#69）：ref 总数/总读取字节有上界，超限条目显式
// 标注「预算截断」并降级 unverified。
func VerifyFindingsEvidence(mf *MergedFindings, changed, touched []string, r SourceReader) (verified, total int) {
	return verifyFindingsEvidenceBudgeted(mf, changed, touched, r, defaultEvidenceBudget())
}

// verifyFindingsEvidenceBudgeted 批量核验的内部入口（测试注入小预算钉住
// 「整批共享一个任务预算」语义）。
func verifyFindingsEvidenceBudgeted(mf *MergedFindings, changed, touched []string, r SourceReader, budget *evidenceBudget) (verified, total int) {
	if mf == nil {
		return 0, 0
	}
	for i := range mf.Findings {
		f := &mf.Findings[i]
		res := verifyFindingEvidenceBudgeted(*f, changed, touched, r, budget)
		f.EvidenceStatus = res.Status
		total++
		if res.Verified {
			verified++
			continue
		}
		if res.Note != "" && !strings.Contains(f.Evidence, evidenceWarnMark) {
			f.Evidence += res.Note
		}
		// Ruling（spec §3 降级句收窄）：只降「给了引用但核验不过」（invalid /
		// 有引用的 unverified=证据对不上现实）；unverified-未提供结构化引用
		//（空/纯文字）只标不降——协议明许「无硬证据时省略字段」，全降会惩罚
		// 诚实省略并激励编造 file:line 换 verified（与阶段1「Evidence 可选」
		// 产品语义冲突）。
		if HighRiskFindingSeverity(f.Severity) &&
			(res.Status == domain.EvidenceInvalid ||
				(res.Status == domain.EvidenceUnverified && len(res.Refs) > 0)) {
			f.Severity = domain.SeverityMedium
		}
	}
	return verified, total
}
