// push_resolver.go —— push 型未知态的自动对账裁决（#64）：用 git 层的下游事实
// 判定「修复分支推送」是否已生效。
//
// 判定边界（与 comment_resolver.go 同纪律）：
//
//   - Key 形态（编码器在 internal/runner pushEffectAction，回环测试互钉）：
//
//     fix:push:<taskID>:<headSHA>:<mode>
//
//   - 资源形态：`branch:<platform>:<owner>/<repo>:<targetBranch>`——目标分支就在
//     资源串里（定界见 ParseBranchResource），无需 payload（resolver 拿到的
//     UncertainSideEffect 只有 payload_hash，没有 payload 明文）。
//
//   - branch 模式（v7.0 缺省形态）：修复分支由该推送创建（argus/fix/<PR号>），
//     远端分支**存在 = 已生效**（tip SHA 入结果载荷）；不存在 = 显式未生效证据
//     （查询成功且无标记），push 是 Unsafe 动作，带 ForceRetryable 签字放行。
//
//   - direct 模式（旧形态）：目标分支是 PR 源分支、本就存在，分支存在性证明不了
//     什么——在近期提交里找带任务标记的 bot 提交（TaskCommitOnBranch）。命中 =
//     已生效；近期历史无标记 = 显式未生效证据（诚实边界：标记提交被推到翻页
//     深度之外时会误判未生效，direct 形态本就是降级路径，风险可接受）。
//
//   - 平台查询失败返回 error（判据不足，本轮不裁决，下一轮再查）。
//
//   - 未知 mode / 坏 Key / 坏 Resource 返回 ok=false（跳过并留痕，不改状态）。
package effect

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/yi-nology/goagent-kernel/sideeffect"

	"github.com/yi-nology/argus/internal/storage"
)

// PushProbe push 型未知态的下游事实探针（git/平台层，生产装配在 bootstrap）。
// 两个判据分别服务 branch/direct 两种推送形态；探测失败（err≠nil）= 判据不足，
// 对账器本轮不裁决。
type PushProbe interface {
	// BranchTipSHA 返回远端分支 tip SHA；ok=false = 分支不存在（查询成功）。
	BranchTipSHA(ctx context.Context, platform, owner, repo, branch string) (sha string, ok bool, err error)
	// TaskCommitOnBranch 在目标分支近期提交里找任务标记的 bot 提交（commit
	// 作者为 argus-bot 且消息含 Task: <taskID>）；ok=false = 近期历史无标记。
	TaskCommitOnBranch(ctx context.Context, platform, owner, repo, branch, taskID string) (sha string, ok bool, err error)
}

// PushResolver push 型 Key 的对账裁决器。
type PushResolver struct {
	probe PushProbe
}

// NewPushResolver 组装 push 裁决器。probe 为 nil 时所有 Key 都跳过（ok=false），
// 不会把「没装探测能力」误判成「分支不存在」。
func NewPushResolver(p PushProbe) *PushResolver {
	return &PushResolver{probe: p}
}

// pushResolutionPayload 已生效时的结果载荷（branch/direct 共用，字段按判据取值）。
type pushResolutionPayload struct {
	Branch string `json:"branch"`
	Tip    string `json:"tip,omitempty"`    // branch 模式：远端 tip SHA
	Commit string `json:"commit,omitempty"` // direct 模式：命中的任务标记提交 SHA
}

// pushKey 解析后的 push 型副作用 Key。
type pushKey struct {
	taskID string
	head   string
	mode   string // branch / direct
}

// parsePushKey 解析 Key（段数精确匹配；任务 ID 不含冒号，head 允许为空）：
//
//	fix:push:<taskID>:<headSHA>:<mode>
func parsePushKey(key string) (pushKey, bool) {
	parts := strings.Split(key, ":")
	if len(parts) != 5 || parts[0] != "fix" || parts[1] != "push" || parts[2] == "" {
		return pushKey{}, false
	}
	switch parts[4] {
	case "branch", "direct":
		return pushKey{taskID: parts[2], head: parts[3], mode: parts[4]}, true
	default:
		return pushKey{}, false
	}
}

// ParseBranchResource 解析分支资源（#64：push 对账需要仓库三元组 + 目标分支；
// 编码器在 internal/runner pushEffectResource，回环测试互钉）：
//
//	branch:<platform>:<owner>/<repo>:<branch>
//
// 定界安全：git refname 不允许 ':'，分支段以**末个** ':' 定界（owner/repo 可含
// '/'，GitLab subgroup）。ok=false = 不可解析。
func ParseBranchResource(resource string) (platform, owner, repo, branch string, ok bool) {
	const prefix = "branch:"
	if !strings.HasPrefix(resource, prefix) {
		return "", "", "", "", false
	}
	rest := strings.TrimPrefix(resource, prefix)
	i := strings.LastIndexByte(rest, ':')
	if i < 0 {
		return "", "", "", "", false
	}
	platform, owner, repo, ok = splitScopedRepo(rest[:i])
	if !ok {
		return "", "", "", "", false
	}
	branch = rest[i+1:]
	if branch == "" {
		return "", "", "", "", false
	}
	return platform, owner, repo, branch, true
}

// Resolve 判定一条未知态 Key。第二个返回值表示「这个 Key 类型是否受支持」：
// false = 跳过（未知前缀/坏 Key/坏 Resource），不改变状态。
func (r *PushResolver) Resolve(ctx context.Context, u storage.UncertainSideEffect) (sideeffect.Resolution, bool, error) {
	if r == nil || r.probe == nil {
		return sideeffect.Resolution{}, false, nil
	}
	pk, ok := parsePushKey(u.Key)
	if !ok {
		return sideeffect.Resolution{}, false, nil
	}
	platform, owner, repo, branch, ok := ParseBranchResource(u.Resource)
	if !ok {
		return sideeffect.Resolution{}, false, nil
	}
	switch pk.mode {
	case "branch":
		// 修复分支由推送创建：远端存在 = 已生效（tip 入载荷可追溯）。
		tip, found, err := r.probe.BranchTipSHA(ctx, platform, owner, repo, branch)
		if err != nil {
			return sideeffect.Resolution{}, true, fmt.Errorf("effect: 探测修复分支失败: %w", err)
		}
		if found {
			return pushAppliedResolution(branch, pushResolutionPayload{Branch: branch, Tip: tip},
				"fix branch present on remote"), true, nil
		}
		return pushNotApplied("fix branch absent on remote"), true, nil
	default: // direct
		// 目标分支是 PR 源分支（本就存在）：在近期提交里找任务标记提交。
		sha, found, err := r.probe.TaskCommitOnBranch(ctx, platform, owner, repo, branch, pk.taskID)
		if err != nil {
			return sideeffect.Resolution{}, true, fmt.Errorf("effect: 检索目标分支提交失败: %w", err)
		}
		if found {
			return pushAppliedResolution(branch, pushResolutionPayload{Branch: branch, Commit: sha},
				"fix commit found on target branch"), true, nil
		}
		return pushNotApplied("fix commit absent on target branch (recent history)"), true, nil
	}
}

// pushAppliedResolution 已生效裁决（Applied=true，载荷落库可重放）。
func pushAppliedResolution(branch string, p pushResolutionPayload, reason string) sideeffect.Resolution {
	raw, merr := json.Marshal(p)
	if merr != nil { // 全 string 字段不可能失败；保守按判据不足处理
		return sideeffect.Resolution{}
	}
	return sideeffect.Resolution{
		Applied: true,
		Result:  sideeffect.Result{Payload: raw},
		Reason:  reason,
	}
}

// pushNotApplied 未生效裁决：push 无平台幂等键（Unsafe），查询成功且无标记是
// 显式证据，显式签字放行重试。
func pushNotApplied(reason string) sideeffect.Resolution {
	return sideeffect.Resolution{
		Applied:        false,
		ForceRetryable: true,
		Reason:         reason,
	}
}
