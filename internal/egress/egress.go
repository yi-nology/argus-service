// Package egress —— LLM 出站治理（T7）：租户/部署级同意门、凭据双通道掩码、
// 哈希清单审计。
//
// 同意门（ARGUS_LLM_EGRESS_CONSENT）：
//
//	"" / allow   缺省：允许外发（兼容存量），启动告警提示未显式同意
//	deny         仅允许 loopback 端点；外部 LLM 配置视为未授权
//	require      外部端点必须再设 =yes 才放行；loopback 免检
//	yes          显式同意外发
//
// 掩码（MaskCredentials）：正则族（AKIA/ghp_/sk-/Bearer 等）+ Shannon 熵
// 通道（>3.5 的长随机串）双通道；命中即打码，不回传原文。
//
// 审计（AuditLLM）：只落 stage + 字节长度 + sha256 前 16 hex（零原文副本）。
package egress

import (
	"crypto/sha256"
	"encoding/hex"
	"math"
	"net"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"

	"github.com/oekit/logx"
)

// ConsentMode 同意门模式词表。
const (
	ConsentAllow   = "allow"
	ConsentDeny    = "deny"
	ConsentRequire = "require"
	ConsentYes     = "yes"
	ConsentEnv     = "ARGUS_LLM_EGRESS_CONSENT"
)

// ModeFromEnv 解析同意门模式（缺省 allow）。
func ModeFromEnv() string {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(ConsentEnv))) {
	case "deny", "disabled", "off", "0", "false":
		return ConsentDeny
	case "require", "needed":
		return ConsentRequire
	case "yes", "granted", "1", "true":
		return ConsentYes
	default:
		return ConsentAllow
	}
}

// IsLoopbackURL 端点是否 loopback（localhost/127.*::1）。解析失败 = 非 loopback
// （fail-closed：认不出就当外部）。
func IsLoopbackURL(raw string) bool {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return true // 无端点 = 无外发
	}
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	host := u.Hostname()
	if host == "" {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// ExternalAllowed 判断给定 LLM base URL 是否允许出站。
// 返回 (allowed, reason)；reason 非空时调用方应禁用/拒绝该端点。
func ExternalAllowed(mode, baseURL string) (bool, string) {
	if IsLoopbackURL(baseURL) {
		return true, ""
	}
	switch mode {
	case ConsentDeny:
		return false, "ARGUS_LLM_EGRESS_CONSENT=deny：仅允许 loopback LLM 端点"
	case ConsentRequire:
		if ModeFromEnv() == ConsentYes { // allow require 模式下再看显式 yes —— mode 传入即 require 时看 env
			return true, ""
		}
		// require 且未显式 yes
		if strings.ToLower(strings.TrimSpace(os.Getenv(ConsentEnv))) != ConsentYes {
			return false, "ARGUS_LLM_EGRESS_CONSENT=require 且未设 =yes：外部 LLM 未获出站同意"
		}
		return true, ""
	case ConsentYes:
		return true, ""
	default: // allow
		return true, ""
	}
}

// ---------- 凭据双通道掩码 ----------

var (
	reAWSKey = regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`)
	reGitHub = regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{20,}\b`)
	reSlack  = regexp.MustCompile(`\bxox[baprs]-[A-Za-z0-9-]{10,}\b`)
	reSkKey  = regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{20,}\b`)
	reBearer = regexp.MustCompile(`(?i)\bBearer\s+[A-Za-z0-9._\-]{20,}\b`)
	rePEM    = regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*?-----END [A-Z ]*PRIVATE KEY-----`)
	reJWT    = regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\b`)
	// 高熵候选：≥32 位的 [A-Za-z0-9+/=_-] 串（不整词边界，避免拆散句子）。
	reHighEntropy = regexp.MustCompile(`[A-Za-z0-9+/=_-]{32,}`)
)

// entropyThreshold Shannon 熵阈值（bit/char）；>3.5 视为随机凭据形态。
const entropyThreshold = 3.5

// shannonEntropy 字符香农熵。
func shannonEntropy(s string) float64 {
	if s == "" {
		return 0
	}
	var freq [256]int
	n := 0
	for i := 0; i < len(s); i++ {
		freq[s[i]]++
		n++
	}
	var h float64
	for _, c := range freq {
		if c == 0 {
			continue
		}
		p := float64(c) / float64(n)
		h -= p * math.Log2(p)
	}
	return h
}

// looksLikeSecret 高熵通道：长度≥32、熵>3.5、含数字，且**不是**已知非凭据
// 形态（git sha/md5/sha256 纯小写 hex；纯小写 hex 不掩——误伤 commit 哈希）。
// 另要求含大写或非字母数字（随机凭据常见；git sha 恒小写 hex）。
func looksLikeSecret(tok string) bool {
	if len(tok) < 32 {
		return false
	}
	// 已知哈希/摘要形态：纯 hex 且长度 32/40/64 → 不是凭据。
	if isKnownDigest(tok) {
		return false
	}
	hasDigit, hasUpper, hasSpecial := false, false, false
	for i := 0; i < len(tok); i++ {
		c := tok[i]
		switch {
		case c >= '0' && c <= '9':
			hasDigit = true
		case c >= 'A' && c <= 'Z':
			hasUpper = true
		case c >= 'a' && c <= 'z':
			// lower
		default:
			hasSpecial = true
		}
	}
	if !hasDigit {
		return false
	}
	if !hasUpper && !hasSpecial {
		return false // 纯小写+数字：更像哈希/ID，不掩
	}
	return shannonEntropy(tok) > entropyThreshold
}

// isKnownDigest 纯小写 hex 且长度为常见摘要长度。
func isKnownDigest(tok string) bool {
	n := len(tok)
	if n != 32 && n != 40 && n != 64 && n != 96 && n != 128 {
		return false
	}
	for i := 0; i < n; i++ {
		c := tok[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// MaskCredentials 双通道凭据掩码：正则族 + 高熵。命中替换为 [REDACTED:<len>]。
// 幂等（已掩码无再命中）。零命中时原样返回（不拷贝大字符串以外的分配）。
func MaskCredentials(s string) string {
	if s == "" {
		return s
	}
	out := s
	replaced := false
	for _, re := range []*regexp.Regexp{rePEM, reAWSKey, reGitHub, reSlack, reSkKey, reBearer, reJWT} {
		if re.MatchString(out) {
			out = re.ReplaceAllStringFunc(out, func(m string) string {
				return "[REDACTED:" + itoa(len(m)) + "]"
			})
			replaced = true
		}
	}
	// 高熵通道（在正则族之后，避免重复处理已红acted 串——红acted 不含 32 位随机）。
	if reHighEntropy.MatchString(out) {
		out = reHighEntropy.ReplaceAllStringFunc(out, func(m string) string {
			if looksLikeSecret(m) {
				replaced = true
				return "[REDACTED:" + itoa(len(m)) + "]"
			}
			return m
		})
	}
	_ = replaced
	return out
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [12]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// ---------- 哈希清单审计 ----------

// AuditLLM 落一条出站审计：stage + payload 字节 + sha256 前 16 hex。
// **不落原文**（零源码/提示词副本）。log 为 nil 时 no-op。
func AuditLLM(log logx.Logger, stage string, payload []byte) {
	if log == nil {
		return
	}
	sum := sha256.Sum256(payload)
	log.Info("argus.egress.llm",
		"stage", stage,
		"bytes", len(payload),
		"sha256_16", hex.EncodeToString(sum[:8]),
	)
}

// ---------- 启动期同意门（供 bootstrap 调用）----------

var consentOnce sync.Once

// CheckLLMConsent 启动期检查：mode + baseURL → (allow, reason)。
// allow 时若 mode=allow 且 baseURL 为外部，打一次告警（未显式同意）。
func CheckLLMConsent(log logx.Logger, baseURL string) (bool, string) {
	mode := ModeFromEnv()
	ok, reason := ExternalAllowed(mode, baseURL)
	if ok && mode == ConsentAllow && !IsLoopbackURL(baseURL) && baseURL != "" {
		consentOnce.Do(func() {
			if log != nil {
				log.Warn("argus.egress.consent_not_explicit",
					"hint", "外部 LLM 出站未显式同意；设 ARGUS_LLM_EGRESS_CONSENT=deny|require|yes 收紧或确认",
					"base_url_host", hostOf(baseURL))
			}
		})
	}
	if !ok && log != nil {
		log.Warn("argus.egress.consent_denied", "reason", reason, "base_url_host", hostOf(baseURL))
	}
	return ok, reason
}

func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "(unparsable)"
	}
	return u.Hostname()
}
