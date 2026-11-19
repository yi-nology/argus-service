// Package gitdeepen 浅克隆工作副本按需历史补全的门面（v3.80.0 实现下沉
// oagentkit/tools/workcopy——工作副本生命周期属主收编 deepen 语义；本包保留 API
// 门面，ocrshim/ocrdeleg 两个调用方零改动）。
//
// 语义：先本地探测 merge-base 可达性（零网络），不可达且 .git/shallow 存在
// 再 `git fetch --deepen` 指数加深（64 起步 ×2）；详见 workcopy 包文档。
package gitdeepen

import "github.com/oagentkit/tools/workcopy"

// EnsureMergeBase 确保 origin/<baseBranch> 与 HEAD 的 merge-base 可达。
var EnsureMergeBase = workcopy.EnsureMergeBase

// EnsureMergeBaseTLS EnsureMergeBase 的 TLS 形态：insecureTLS=true 时对 deepen
// fetch 注入 GIT_SSL_NO_VERIFY（v5.4.7 funcdiff 消费；与 workcopy 克隆/OCR 通道
// 对自签平台的纪律对齐——漏注会让自签平台的 deepen 确定性失败且静默降级）。
var EnsureMergeBaseTLS = workcopy.EnsureMergeBaseTLS

// DeepenOption 深度补全可选项（workcopy 同源）。
type DeepenOption = workcopy.DeepenOption

// WithToken deepen fetch 的鉴权 token（tools#160）：tools#124 起 clone/PR
// fetch/refresh 均走无凭证 URL + 进程级凭证注入，deepen 是唯一漏注的 fetch——
// 私有远端浅克隆加深必 401，函数级 diff 每次静默降级。带 Token 时必须同时
// 提供期望 clone 源（WithCloneSource，tools#184）。
var WithToken = workcopy.WithToken

// WithCloneSource deepen fetch 的期望 clone 源（tools#184）：baseURL/owner/repo
// 齐备时目标 URL 由 cloneURL 现算、不读 .git/config——工作树写手篡改 origin
// 的 token 外带通道在该形态下关闭。
var WithCloneSource = workcopy.WithCloneSource

// Auth deepen fetch 的鉴权与期望 clone 源（tools#160/#184）。与 workcopy 克隆
// 同源解析（Pool.CredentialOf）：Token 为空 = 无鉴权面（公有远端/file://
// fixture）。带 Token 时 BaseURL/Owner/Repo 必须齐备——期望源缺位时 Options
// 丢弃 token（fail-closed：旧形态下鉴权 scheme 从可被工作树写手篡改的 origin
// URL 反推，token 外带通道仍开放）。
type Auth struct {
	Token   string
	BaseURL string
	Owner   string
	Repo    string
}

// Options 折叠为 deepen 可选项：无 token 空转；token 而期望 clone 源不齐时
// 丢弃 token（fail-closed，见类型注释）。
func (a Auth) Options() []DeepenOption {
	if a.Token == "" || a.BaseURL == "" || a.Owner == "" || a.Repo == "" {
		return nil
	}
	return []DeepenOption{
		WithToken(a.Token),
		WithCloneSource(a.BaseURL, a.Owner, a.Repo),
	}
}
