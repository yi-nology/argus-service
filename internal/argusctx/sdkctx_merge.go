// sdkctx_merge.go —— 受控合并与 CI 资格取数的 SDK 薄封装。
//
// MergeCR 走 provider.ChangeRequestManager（全平台必有）；CI 走
// CommitStatusManager 可选能力——缺能力或读取失败一律返回 unknown，
// 合并门 fail-closed（绝不把「取不到」当成「通过」）。
package argusctx

import (
	"context"
	"time"

	"github.com/yi-nology/go-git-platform/model"
	"github.com/yi-nology/go-git-platform/provider"
)

// MergeCR 合并变更请求（受控合并 confirm 的执行体；平台保护规则照常生效）。
func (s *SDKCtx) MergeCR(ctx context.Context, platform, owner, repo, number string, opts provider.MergeCROptions) (*model.ChangeRequest, error) {
	return call(s, ctx, platform, func(ctx context.Context, p provider.Provider) (*model.ChangeRequest, error) {
		return p.MergeCR(ctx, owner, repo, number, opts)
	})
}

// CIState 合并门 CI 判定词表（与 model.CommitStatusState 对齐 + unknown）。
const (
	CIStateSuccess = "success"
	CIStateFailure = "failure"
	CIStatePending = "pending"
	CIStateUnknown = "unknown" // 无能力 / 读失败 / 空列表 —— fail-closed
)

// EvalCIStatus 合并资格门的 CI 判定：CommitStatusManager 双面中的
// Commit Statuses 面（provider 未暴露 Check Runs 独立 API；Gitee 适配层
// 已把 check runs 折进 statuses）。返回 (state, detail)：
//   - 无能力 / 调用失败 / 空列表 → ("unknown", …)：调用方必须阻断；
//   - CombineCommitStatusStates 取最坏：任一 failure/error/canceled → failure；
//     任一 pending → pending；全部 success → success。
//
// T16：GitHub 双面判定（Commit Statuses + Check Runs）已补齐——两面取最坏；
// 其余平台仍以 Commit Statuses 为准（Gitee 适配层已折 check runs 进 statuses）。
func (s *SDKCtx) EvalCIStatus(ctx context.Context, platform, owner, repo, sha string) (state, detail string) {
	if sha == "" || s == nil {
		return CIStateUnknown, "missing head sha"
	}
	p, err := s.Provider(platform)
	if err != nil || p == nil {
		return CIStateUnknown, "provider unavailable"
	}
	cs, ok := p.(provider.CommitStatusManager)
	if !ok || !p.Capabilities().CommitStatuses {
		return CIStateUnknown, "commit statuses unsupported"
	}
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	statuses, err := cs.ListCommitStatuses(cctx, owner, repo, sha)
	if err != nil {
		// 读失败仍继续看 check-runs 面（双面取最坏）。
		statuses = nil
	}
	statusState, statusDetail := CIStateUnknown, "no ci statuses on head"
	if len(statuses) > 0 {
		states := make([]model.CommitStatusState, len(statuses))
		for i, st := range statuses {
			states[i] = st.State
		}
		combined := model.CombineCommitStatusStates(states)
		switch combined {
		case model.CommitStatusSuccess:
			statusState, statusDetail = CIStateSuccess, "all statuses success"
		case model.CommitStatusPending, model.CommitStatusRunning:
			statusState, statusDetail = CIStatePending, "statuses pending"
		default:
			statusState, statusDetail = CIStateFailure, "statuses "+string(combined)
		}
	}

	// T16 第二面：GitHub Check Runs（Actions-only 仓此前被误判 unknown）。
	if platform == "github" {
		crState, crDetail := s.evalGitHubCheckRuns(ctx, owner, repo, sha)
		merged := combineCI(statusState, crState)
		return merged, "statuses=[" + statusDetail + "] check_runs=[" + crDetail + "]"
	}
	return statusState, statusDetail
}
