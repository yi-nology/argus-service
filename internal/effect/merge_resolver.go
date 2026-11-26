// merge_resolver.go —— 合并型未知态的读回对账：按平台 CR 实际状态定案。
//
// 判定边界：
//   - Key 形态 merge:<op_id>；Resource 形态 merge:<platform>:<owner>/<repo>#<n>
//     （或带租户前缀 <tid>/merge:…）；
//   - GetCR 成功且 State=merged → Applied（payload 含 platform 状态）；
//   - GetCR 成功且 State=opened → 显式未生效（Unsafe 动作需 ForceRetryable）；
//   - State=closed → 视为 failed（非合并终态，不可当 applied）；
//   - 查询失败 → error（判据不足，下轮再查）；未知前缀 → ok=false 跳过。
package effect

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/yi-nology/go-git-platform/model"
	"github.com/yi-nology/goagent-kernel/sideeffect"

	"github.com/yi-nology/argus/internal/storage"
)

// MergeCRFinder 按坐标读 CR 当前状态（argusctx.SDKCtx.GetCR 的接口投影）。
type MergeCRFinder interface {
	GetCR(ctx context.Context, platform, owner, repo, number string) (*model.ChangeRequest, error)
}

// MergeResolver 合并型 Key 的对账裁决器。
type MergeResolver struct {
	finder MergeCRFinder
}

// NewMergeResolver 组装合并裁决器。finder 为 nil 时全部跳过（ok=false）。
func NewMergeResolver(f MergeCRFinder) *MergeResolver {
	return &MergeResolver{finder: f}
}

// mergeResolutionPayload 已生效/已关闭时的结果载荷。
type mergeResolutionPayload struct {
	Platform string `json:"platform"`
	State    string `json:"state"`
	HeadSHA  string `json:"head_sha,omitempty"`
}

// Resolve implements UncertainResolver。第二个返回值 = 这个 Key 类型是否受支持。
func (r *MergeResolver) Resolve(ctx context.Context, u storage.UncertainSideEffect) (sideeffect.Resolution, bool, error) {
	if r == nil || r.finder == nil {
		return sideeffect.Resolution{}, false, nil
	}
	if !strings.HasPrefix(u.Key, "merge:") {
		return sideeffect.Resolution{}, false, nil
	}
	platform, owner, repo, number, ok := ParseMergeResource(u.Resource)
	if !ok {
		return sideeffect.Resolution{}, false, nil
	}
	cr, err := r.finder.GetCR(ctx, platform, owner, repo, number)
	if err != nil {
		return sideeffect.Resolution{}, true, fmt.Errorf("effect: 查询合并目标 CR 失败: %w", err)
	}
	if cr == nil {
		return sideeffect.Resolution{}, true, fmt.Errorf("effect: 合并目标 CR 为空: %s", u.Key)
	}
	switch cr.State {
	case model.CRStateMerged:
		raw, merr := json.Marshal(mergeResolutionPayload{
			Platform: platform, State: "merged", HeadSHA: cr.HeadSHA,
		})
		if merr != nil {
			return sideeffect.Resolution{}, true, fmt.Errorf("effect: 编码合并结果失败: %w", merr)
		}
		return sideeffect.Resolution{
			Applied: true,
			Result:  sideeffect.Result{Payload: raw},
			Reason:  "merge already applied on platform",
		}, true, nil
	case model.CRStateOpened:
		// 显式未生效：合并是 Unsafe 动作，重试须人工 ForceRetryable 签字。
		return sideeffect.Resolution{
			Applied:        false,
			ForceRetryable: true,
			Reason:         "merge target still opened",
		}, true, nil
	case model.CRStateClosed:
		raw, _ := json.Marshal(mergeResolutionPayload{
			Platform: platform, State: "closed",
		})
		return sideeffect.Resolution{
			Applied: false,
			Result:  sideeffect.Result{Payload: raw},
			Reason:  "merge target closed without merge",
		}, true, nil
	default:
		return sideeffect.Resolution{}, true, fmt.Errorf("effect: 未知 CR 状态 %q", string(cr.State))
	}
}

// ParseMergeResource merge 资源串 → 三元组 + PR 号。
// 形态：merge:<platform>:<owner>/<repo>#<n>（可带 <tid>/ 前缀）。
func ParseMergeResource(resource string) (platform, owner, repo, number string, ok bool) {
	if i := strings.IndexByte(resource, '/'); i > 0 && !strings.Contains(resource[:i], ":") {
		resource = resource[i+1:] // strip tenant prefix
	}
	if !strings.HasPrefix(resource, "merge:") {
		return "", "", "", "", false
	}
	rest := strings.TrimPrefix(resource, "merge:")
	hash := strings.LastIndexByte(rest, '#')
	if hash <= 0 || hash == len(rest)-1 {
		return "", "", "", "", false
	}
	number = rest[hash+1:]
	rest = rest[:hash]
	c := strings.IndexByte(rest, ':')
	if c <= 0 {
		return "", "", "", "", false
	}
	platform, scoped := rest[:c], rest[c+1:]
	slash := strings.IndexByte(scoped, '/')
	if slash <= 0 || slash == len(scoped)-1 {
		return "", "", "", "", false
	}
	return platform, scoped[:slash], scoped[slash+1:], number, true
}

// EncodeMergeResource 构造 merge 资源串（租户前缀可选）。
func EncodeMergeResource(tenantID, platform, owner, repo, number string) string {
	base := "merge:" + platform + ":" + owner + "/" + repo + "#" + number
	if tenantID == "" || tenantID == "default" {
		return base
	}
	return tenantID + "/" + base
}
