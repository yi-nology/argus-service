package domain

// 组织级误报判例桶（批 E spec）：仓无关 Pattern 投影的误报模式共享。
// 一期语义=同实例跨仓（org 轴不跨——不同组织信任边界不同）；跨实例留二期。

import (
	"crypto/sha256"
	"encoding/hex"
	"path"
	"regexp"
	"strings"
)

// FPCaseStatus 判例确认态：candidate（机器产候选，只出现在待审清单）→
// confirmed（人工确认，参与审查面提示）/ rejected（人工否决）。
const (
	FPCaseCandidate = "candidate"
	FPCaseConfirmed = "confirmed"
	FPCaseRejected  = "rejected"
)

// fpPatternNoise comment 骨架归一的剥离面：数字/引号内容/常见标识符字面量。
var (
	fpPatternNum    = regexp.MustCompile(`\d+`)
	fpPatternQuoted = regexp.MustCompile(`"[^"]*"|'[^']*'`)
	fpPatternPath   = regexp.MustCompile(`[\w./-]+\.[A-Za-z0-9]{1,8}:\d+`) // file:line 引用
)

// FPCasePattern 仓无关指纹投影（批 E §3.2，确定性纯函数）：
//
//	pattern = sha256(platform|org|归一file|归一comment)
//
// 归一 file = 去目录层级文件名+扩展名（auth/login.go → login.go）；
// 归一 comment = 去 file:line 引用/数字/引号内容后的骨架。
// org 轴不跨（GitLab group/GitHub org 隔离）；同模式跨仓同 Pattern。
func FPCasePattern(platform, org, file, comment string) string {
	nf := normalizeFPFile(file)
	nc := normalizeFPComment(comment)
	h := sha256.Sum256([]byte(platform + "|" + org + "|" + nf + "|" + nc))
	return hex.EncodeToString(h[:])
}

// normalizeFPFile 文件名归一：去目录层级（保扩展名）。
func normalizeFPFile(f string) string {
	f = strings.TrimSpace(f)
	if f == "" {
		return ""
	}
	return path.Base(f)
}

// normalizeFPComment 评语骨架归一：先去 file:line 引用（含归一前的路径
// 差异），再去数字与引号内容——跨仓同措辞收敛到同骨架。
func normalizeFPComment(c string) string {
	c = fpPatternPath.ReplaceAllString(c, "<ref>")
	c = fpPatternQuoted.ReplaceAllString(c, `"*"`)
	c = fpPatternNum.ReplaceAllString(c, "<n>")
	return strings.Join(strings.Fields(c), " ")
}
