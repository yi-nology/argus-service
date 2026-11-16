package plugin

// 证据内容对账与四态判定核心（v7.5 #68/#69）：从 evidence.go 拆出，
// 保持单文件在架构阈值（400 行）以内。

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/yi-nology/argus/internal/domain"
)

// evidenceContent 证据内容对账期望（v7.5 #68，Finding 级可选输入）：
// Snippet=模型照抄的引用行片段；SHA256=引用行归一内容的摘要（自动化上游用）。
// 两者都空 = 未提供内容期望 → verified 不可达（上限 structurally_valid）。
type evidenceContent struct {
	Snippet string
	SHA256  string
}

// empty 是否未提供任何内容期望。
func (c evidenceContent) empty() bool { return c.Snippet == "" && c.SHA256 == "" }

// assessEvidenceStatus Finding 级四态核验核心（Phase 2 + v7.5 #68 内容对账）：
// 解析引用 → 逐 ref 判定 → 聚合取最差（domain.EvidenceStatusRank）。与 Claim
// 级 verifyEvidenceText 并存互不改（Claim 语义零动）：
//   - 无引用（纯文字/空）→ unverified「未提供结构化证据」（不再叫 verified）
//   - reader==nil → 集合级：在改动/触达集=structural，否则 unverified
//   - reader 读文件：fs.ErrNotExist→invalid；行号超界/空行/纯注释→unverified；
//     预算超限（ErrSourceFileTooLarge/任务预算）→unverified + 显式标注；
//     基础设施错误（非 NotExist）→集合级降级（不冤枉）；
//     结构全对 + HeadSHA 非空 + 内容对账通过→verified，否则 structural。
//
// 内容对账（#68 语义升级）：exp 非空时，至少一个可读引用行的归一内容必须
// 与 Snippet（空白归一后包含）或 SHA256（归一行内容的摘要）一致；全部对不上
// → unverified「内容与引用行不符」。exp 为空时无法对账 → verified 不可达，
// 降为 structurally_valid（引用可定位 ≠ 证据内容属实，不再冒充语义成立）。
//
// 预算（#69）：budget 非 nil 时限制本次批量核验的 ref 总数与总读取字节；
// 超限的 ref 记 unverified +「预算耗尽/截断」原因（不误判 invalid、不静默）。
//
// Ruling（spec §3 行号越界双写消歧）：行号越界归 unverified 非 invalid——
// 文件真实存在、引用指向不存在的行=待人工核实；invalid 留给根本不可评估
// （文件不存在/解析失败）。
func assessEvidenceStatus(s string, changed, touched []string, r SourceReader, exp evidenceContent, budget *evidenceBudget) (domain.EvidenceStatus, []EvidenceRef, string) {
	refs := ParseEvidenceRefs(s)
	if len(refs) == 0 {
		return domain.EvidenceUnverified, nil, " " + evidenceWarnMark + "（未提供结构化证据：无 file:line 引用）"
	}
	inSet := fileSet(changed, touched)
	worst := domain.EvidenceVerified
	var badRefs []EvidenceRef
	var reasons []string
	seen := map[string]bool{}
	readable, matched := false, false
	mismatchReason := ""
	for _, ref := range refs {
		if !budget.allowRef() {
			worst = worseStatus(worst, domain.EvidenceUnverified)
			badRefs = appendRefOnce(badRefs, seen, ref)
			reasons = append(reasons, "任务证据读取预算耗尽（引用数上限），已截断核验")
			continue
		}
		a := assessRef(ref, inSet, r)
		if a.Read || a.Lines != nil {
			if !budget.addFile(ref.File, a.Lines) {
				// 总字节预算在这一文件越界：本次引用按预算截断（不静默放行）。
				a.Status = domain.EvidenceUnverified
				a.Reason = "任务证据读取预算耗尽（总字节上限），已截断核验"
				a.Read = false
			} else if a.Read && !exp.empty() {
				ok, why := evidenceContentMatches(a.Line, exp)
				if ok {
					matched = true
				} else if mismatchReason == "" {
					mismatchReason = why
				}
			}
		}
		if a.Read {
			readable = true
		}
		if domain.EvidenceStatusRank(a.Status) < domain.EvidenceStatusRank(worst) {
			worst = a.Status
		}
		if a.Status == domain.EvidenceInvalid || a.Status == domain.EvidenceUnverified {
			badRefs = appendRefOnce(badRefs, seen, ref)
			if a.Reason != "" {
				reasons = append(reasons, a.Reason)
			}
		}
	}
	switch {
	case exp.empty() && worst == domain.EvidenceVerified:
		// 没有内容期望就没有对账依据：结构/版本都对也只能到 structural。
		worst = domain.EvidenceStructuralValid
	case !exp.empty() && readable && !matched:
		worst = worseStatus(worst, domain.EvidenceUnverified)
		for _, ref := range refs {
			badRefs = appendRefOnce(badRefs, seen, ref)
		}
		reasons = append(reasons, mismatchReason)
	}
	if worst == domain.EvidenceStructuralValid || worst == domain.EvidenceVerified {
		return worst, refs, ""
	}
	note := " " + evidenceWarnMark + "（"
	for i, r := range badRefs {
		if i > 0 {
			note += "、"
		}
		note += r.File + ":" + strconv.Itoa(r.Line)
	}
	if len(reasons) > 0 {
		note += " " + strings.Join(dedupeStrings(reasons), "；")
	}
	note += "）"
	return worst, refs, note
}

// refAssessment 单 ref 核验结果：状态 + 未通过原因 + 读取到的引用行原文。
// Read=true 表示读取成功且行号在界内，可参与内容对账；Lines 是读取到的
// 完整行表（仅引用，不复制——供任务级字节预算计量，同文件只计一次）。
type refAssessment struct {
	Status domain.EvidenceStatus
	Reason string
	Line   string
	Read   bool
	Lines  []string
}

// assessRef 单 ref 四态判定（见 assessEvidenceStatus 注释的分支语义）。
func assessRef(ref EvidenceRef, inSet map[string]bool, r SourceReader) refAssessment {
	if r == nil {
		if ref.Line > 0 && inSet[ref.File] {
			return refAssessment{Status: domain.EvidenceStructuralValid}
		}
		return refAssessment{Status: domain.EvidenceUnverified, Reason: "不在改动集/触达集，或行号无效"}
	}
	lines, err := r.ReadLines(ref.File)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return refAssessment{Status: domain.EvidenceInvalid, Reason: "文件不存在于工作副本"}
		}
		// #69 预算截断：文件存在但超出读取范围——降级并显式标注，不判 invalid。
		if errors.Is(err, ErrSourceFileTooLarge) {
			return refAssessment{Status: domain.EvidenceUnverified, Reason: "单文件超出读取预算（大小/行数），已截断核验"}
		}
		if errors.Is(err, ErrSourceBudgetExceeded) {
			return refAssessment{Status: domain.EvidenceUnverified, Reason: "任务读取预算耗尽（总字节），已截断核验"}
		}
		// 基础设施性失败（凭证/网络/围栏/超时）：集合级降级，不判 invalid 冤枉证据
		if inSet[ref.File] {
			return refAssessment{Status: domain.EvidenceStructuralValid}
		}
		return refAssessment{Status: domain.EvidenceUnverified, Reason: "读取失败（集合外文件）"}
	}
	if ref.Line > len(lines) {
		return refAssessment{Status: domain.EvidenceUnverified, Reason: "行号超出文件范围", Lines: lines}
	}
	line := lines[ref.Line-1]
	if !isCodeEvidenceLine(ref.File, line) {
		return refAssessment{Status: domain.EvidenceUnverified, Reason: "引用行为空行或纯注释行", Lines: lines}
	}
	if r.HeadSHA() == "" {
		return refAssessment{Status: domain.EvidenceStructuralValid, Line: line, Read: true, Lines: lines}
	}
	// 结构/版本全对；内容对账由聚合层裁决（exp 为空时上限 structural）。
	return refAssessment{Status: domain.EvidenceVerified, Line: line, Read: true, Lines: lines}
}

// evidenceContentMatches 单行内容对账（v7.5 #68）：空白归一后 Snippet 包含
// 比对 + SHA256 摘要比对（同时给出时两者都必须通过）。返回 (是否匹配,
// 不匹配原因)；Snippet/SHA256 非法（空片段/哈希格式错）按不匹配处理。
func evidenceContentMatches(line string, exp evidenceContent) (bool, string) {
	norm := normalizeEvidenceLine(line)
	if exp.Snippet != "" {
		want := normalizeEvidenceLine(exp.Snippet)
		if want == "" {
			return false, "证据片段为空"
		}
		if !strings.Contains(norm, want) {
			return false, "证据内容与引用行不符"
		}
	}
	if exp.SHA256 != "" {
		want, ok := parseSHA256Hex(exp.SHA256)
		if !ok {
			return false, "证据哈希格式无效"
		}
		sum := sha256.Sum256([]byte(norm))
		if hex.EncodeToString(sum[:]) != want {
			return false, "证据摘要与引用行不符"
		}
	}
	return true, ""
}

// normalizeEvidenceLine 空白归一：连续空白（含 tab/全角空格外的一般空白）
// 折叠为单个空格并去首尾——模型照抄片段时的缩进/对齐差异不算内容不符。
func normalizeEvidenceLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// parseSHA256Hex 解析证据摘要：可带 `sha256:` 前缀，大小写不敏感；必须恰为
// 64 位十六进制。
func parseSHA256Hex(s string) (string, bool) {
	s = strings.TrimSpace(strings.ToLower(s))
	s = strings.TrimPrefix(s, "sha256:")
	if len(s) != 64 {
		return "", false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return "", false
		}
	}
	return s, true
}

// isCodeEvidenceLine 轻量语义校验（#68，不引入 LLM）：引用行必须非空且不是
// 纯注释行——指向空行/注释行的证据不再能拿 verified（引用可定位 ≠ 内容属实）。
// 判定刻意保守：只认行首注释前缀，注释符出现在行中就当代码（宁可漏判）。
func isCodeEvidenceLine(path, line string) bool {
	t := strings.TrimSpace(line)
	if t == "" {
		return false
	}
	switch {
	case strings.HasPrefix(t, "//"), strings.HasPrefix(t, "/*"),
		strings.HasPrefix(t, "*/"), strings.HasPrefix(t, "<!--"):
		return false
	case strings.HasPrefix(t, "*"):
		// 块注释续行（`* 说明` / `*/`）；`*p`、`*foo` 是解引用代码。
		return len(t) > 1 && t[1] != ' ' && t[1] != '\t' && t[1] != '/'
	case strings.HasPrefix(t, "#"):
		// C 预处理器是代码不是注释；其余语言行首 # 视为注释（shell/python/yaml）。
		return isCPreprocessorLine(path, t)
	case strings.HasPrefix(t, "--"):
		// SQL/Lua 注释（`-- 说明` / `--`）；`--x` 是自减表达式。
		return len(t) > 2 && t[2] != ' ' && t[2] != '-'
	case strings.HasPrefix(t, ";"):
		// asm/lisp/ini 行首分号注释（主流语言里行首分号不是有效语句）。
		return false
	}
	return true
}

// isCPreprocessorLine 行首 # 是否为 C/C++ 预处理指令（是 → 代码行）。
func isCPreprocessorLine(path, t string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".c", ".h", ".cc", ".cpp", ".cxx", ".hpp", ".hxx", ".m", ".mm", ".cu", ".cuh":
	default:
		return false
	}
	directives := []string{
		"#include", "#define", "#if", "#ifdef", "#ifndef", "#else", "#elif",
		"#endif", "#pragma", "#error", "#warning", "#undef", "#line", "#import",
	}
	for _, d := range directives {
		if t == d || strings.HasPrefix(t, d+" ") || strings.HasPrefix(t, d+"\t") ||
			strings.HasPrefix(t, d+"<") || strings.HasPrefix(t, d+"\"") {
			return true
		}
	}
	return false
}

// appendRefOnce 去重追加（同一 file:line 只点名一次）。
func appendRefOnce(refs []EvidenceRef, seen map[string]bool, ref EvidenceRef) []EvidenceRef {
	key := ref.File + ":" + strconv.Itoa(ref.Line)
	if seen[key] {
		return refs
	}
	seen[key] = true
	return append(refs, ref)
}

// worseStatus 两态取更差（rank 小者差）。
func worseStatus(a, b domain.EvidenceStatus) domain.EvidenceStatus {
	if domain.EvidenceStatusRank(b) < domain.EvidenceStatusRank(a) {
		return b
	}
	return a
}

// dedupeStrings 保序去重（同一原因只出现一次）。
func dedupeStrings(in []string) []string {
	if len(in) < 2 {
		return in
	}
	seen := make(map[string]bool, len(in))
	out := in[:0]
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}
