// cr_resolver.go —— 修复 PR 创建型未知态的自动对账裁决（#64）：按源分支查既有
// PR（不限状态）确认动作是否已生效。
//
// 判定边界（与 comment_resolver.go / push_resolver.go 同纪律）：
//
//   - Key 形态（编码器在 internal/runner crEffectAction，回环测试互钉）：
//
//     fix:cr:<taskID>:<headSHA>:branch
//
//   - 资源形态：`pr:<platform>:<owner>/<repo>#<number>`（parsePRResource 单源）。
//     源分支**不在**资源串里，resolver 也不持有 payload 明文（UncertainSideEffect
//     只有 hash）——按修复分支命名约定重建：`<FixBranchPrefix><PR号>`（编码器
//     fixBranchName 与本解析回环互钉；PR 号来自资源串）。
//
//   - 查询**不限状态**：open 命中 = 创建已生效且可收敛；closed/merged 命中同样
//     证明创建发生过（重试会重复开 PR）。只查 open 会把「已合并」误判成未生效。
//
//   - 平台查询成功且无命中 = 显式未生效证据（PR 创建是 Unsafe 动作），带
//     ForceRetryable 签字放行。
//
//   - 坏 Key / 坏 Resource 返回 ok=false（跳过并留痕，不改状态）。
package effect

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/yi-nology/goagent-kernel/sideeffect"

	"github.com/yi-nology/argus/internal/storage"
)

// FixBranchPrefix 修复分支命名空间（#62 起在役；#64 对账解析按它重建源分支，
// 编码器在 internal/runner fixBranchName——改动前先看两处回环测试）。
const FixBranchPrefix = "argus/fix/"

// CRFinder 按源分支查既有 PR（poster.FindTaskComment 的同类投影：宿主能力
// 的窄接口）。不限状态——已合并/已关闭的修复 PR 同样证明创建发生过。
type CRFinder interface {
	FindCRBySource(ctx context.Context, platform, owner, repo, sourceBranch string) (crID int64, number, url string, ok bool, err error)
}

// CRResolver 修复 PR 创建型 Key 的对账裁决器。
type CRResolver struct {
	finder CRFinder
}

// NewCRResolver 组装修复 PR 裁决器。finder 为 nil 时所有 Key 都跳过（ok=false）。
func NewCRResolver(f CRFinder) *CRResolver {
	return &CRResolver{finder: f}
}

// crResolutionPayload 已生效时的结果载荷（与 runner 的 crEffectResult 同形，
// Replayed 时 runner 可直接 decodeCRResult 恢复通知/审计所需字段）。
type crResolutionPayload struct {
	CRID   int64  `json:"cr_id"`
	Number string `json:"cr_number,omitempty"`
	URL    string `json:"url,omitempty"`
}

// parseCRKey 解析 Key（段数精确匹配，任务 ID/head 不含冒号；PR 修复创建只在
// branch mode 调用，尾段恒为 branch）：
//
//	fix:cr:<taskID>:<headSHA>:branch
func parseCRKey(key string) (taskID string, ok bool) {
	parts := strings.Split(key, ":")
	if len(parts) != 5 || parts[0] != "fix" || parts[1] != "cr" || parts[2] == "" || parts[4] != "branch" {
		return "", false
	}
	return parts[2], true
}

// crSourceBranch 按修复分支命名约定 + PR 号重建源分支。
func crSourceBranch(number string) string {
	return FixBranchPrefix + number
}

// Resolve 判定一条未知态 Key。第二个返回值表示「这个 Key 类型是否受支持」：
// false = 跳过（未知前缀/坏 Key/坏 Resource），不改变状态。
func (r *CRResolver) Resolve(ctx context.Context, u storage.UncertainSideEffect) (sideeffect.Resolution, bool, error) {
	if r == nil || r.finder == nil {
		return sideeffect.Resolution{}, false, nil
	}
	taskID, ok := parseCRKey(u.Key)
	if !ok {
		return sideeffect.Resolution{}, false, nil
	}
	platform, owner, repo, number, ok := parsePRResource(u.Resource)
	if !ok {
		return sideeffect.Resolution{}, false, nil
	}
	source := crSourceBranch(number)
	crID, number2, url, found, err := r.finder.FindCRBySource(ctx, platform, owner, repo, source)
	if err != nil {
		return sideeffect.Resolution{}, true, fmt.Errorf("effect: 查询修复 PR 失败: %w", err)
	}
	if found {
		if crID <= 0 && number2 == "" && url == "" {
			return sideeffect.Resolution{}, true, fmt.Errorf("effect: 修复 PR 命中但身份全空: task %s", taskID)
		}
		raw, merr := json.Marshal(crResolutionPayload{CRID: crID, Number: number2, URL: url})
		if merr != nil { // string/int64 不可能失败；保守按查询失败处理
			return sideeffect.Resolution{}, true, fmt.Errorf("effect: 编码修复 PR 结果失败: %w", merr)
		}
		return sideeffect.Resolution{
			Applied: true,
			Result:  sideeffect.Result{Payload: raw},
			Reason:  "fix CR exists for source branch",
		}, true, nil
	}
	// 平台查询成功且无命中：动作未生效。修复 PR 创建无平台幂等键（Unsafe），
	// 这里是明确证据，显式签字允许重试。
	return sideeffect.Resolution{
		Applied:        false,
		ForceRetryable: true,
		Reason:         "fix CR absent for source branch " + source,
	}, true, nil
}
