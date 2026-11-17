// runner_impl_sideeffect.go —— 修复链创建型副作用的接线助手（C1）：
// 推送/开 CR 的幂等键、资源标识、结果载荷与惰性装配。
package runner

import (
	"encoding/json"
	"fmt"

	"github.com/yi-nology/go-git-platform/model"

	"github.com/yi-nology/argus/internal/effect"
	"github.com/yi-nology/argus/internal/plugin"
	"github.com/yi-nology/argus/internal/tenant"
)

// pushEffectRunner 惰性装配推送闸（5min TTL）；显式注入优先。
func (r *Runner) pushEffectRunner() *effect.Runner {
	if r.PushEffects != nil {
		return r.PushEffects
	}
	return effect.NewFromStore(r.Store, effect.PushTTL, r.Log, effect.Enabled())
}

// crEffectRunner 惰性装配修复 PR 创建闸（2min TTL）；显式注入优先。
func (r *Runner) crEffectRunner() *effect.Runner {
	if r.Effects != nil {
		return r.Effects
	}
	return effect.NewFromStore(r.Store, effect.DefaultTTL, r.Log, effect.Enabled())
}

// pushEffectResource 分支级资源：同一目标的推送互斥，不同 PR/分支互不阻塞。
// 此前用仓库级资源，同仓不同 PR 的并发修复推送会互相 ErrContended 被丢弃。
// 租户前缀（#83）：资源串是全局命名空间——同坐标跨租户共享一把闸会把 A 的
// 评论/推送判成 B 的 Replayed；tenant.Key 形态 "<tid>/branch:..."。
func pushEffectResource(pr *plugin.PRContext, targetBranch, tid string) string {
	return tenant.Key(tid, "branch:"+pr.Platform+":"+pr.Owner+"/"+pr.Repo+":"+targetBranch)
}

// crEffectResource PR 级资源：同一 PR 的修复 PR 创建互斥，不同 PR 互不阻塞。
func crEffectResource(pr *plugin.PRContext, tid string) string {
	return tenant.Key(tid, fmt.Sprintf("pr:%s:%s/%s#%s", pr.Platform, pr.Owner, pr.Repo, pr.Number))
}

// crEffectResult 修复 PR 创建结果（Replayed 时据此恢复通知/审计所需字段）。
type crEffectResult struct {
	CRID   int64  `json:"cr_id"`
	Number string `json:"cr_number,omitempty"`
	URL    string `json:"url,omitempty"`
}

// encodeCRResult 把平台返回的 CR 身份落成 Result.Payload。
func encodeCRResult(cr *model.ChangeRequest) effect.Result {
	if cr == nil {
		return effect.Result{}
	}
	raw, err := json.Marshal(crEffectResult{CRID: cr.ID, Number: cr.Number, URL: cr.WebURL})
	if err != nil {
		return effect.Result{}
	}
	return effect.Result{Payload: raw}
}

// decodeCRResult 从既有 Result 恢复 CR（Number/URL 至少一个非空才算有效）。
func decodeCRResult(res effect.Result) (*model.ChangeRequest, error) {
	var out crEffectResult
	if len(res.Payload) == 0 {
		return nil, fmt.Errorf("runner: 副作用结果为空，无法恢复修复 PR")
	}
	if err := json.Unmarshal(res.Payload, &out); err != nil {
		return nil, fmt.Errorf("runner: 副作用结果不是合法 JSON: %w", err)
	}
	if out.Number == "" && out.URL == "" && out.CRID == 0 {
		return nil, fmt.Errorf("runner: 副作用结果缺修复 PR 身份")
	}
	return &model.ChangeRequest{ID: out.CRID, Number: out.Number, WebURL: out.URL}, nil
}

// pushEffectAction 分支推送动作。Key 含 task/head/mode：同一逻辑推送；Payload
// 只放稳定身份（mode+目标分支），不放 commit message/验证条数——它们跨重试可能
// 变化，放进身份会把正常重试误判成 ErrActionConflict。RetrySafetyUnsafe：
// git push 无幂等键，失败即结果未知。
// Key 格式冻结（fix:push:<taskID>:... 无租户段）：taskID 全局唯一已排除撞键，
// 且生产既有 side_effect 行按旧键持久化——换格式=孤儿化存量行=重复开 CR/
// 重复推送（与 ActionKeyBuilder 弃项同一裁定）。租户隔离由 Resource 前缀承担。
func pushEffectAction(taskID, headSHA, mode, target string, pr *plugin.PRContext, tid string) effect.Action {
	identity := fmt.Sprintf("fix:push:%s:%s:%s", taskID, headSHA, mode)
	return effect.Action{
		Key:         effect.Key(identity),
		Resource:    pushEffectResource(pr, target, tid),
		Payload:     []byte(identity + ":" + target),
		RetrySafety: effect.RetrySafetyUnsafe,
	}
}

// crEffectAction 修复 PR 创建动作（openFixPR 只在 branch mode 调用）。
func crEffectAction(taskID, headSHA, branch string, pr *plugin.PRContext, tid string) effect.Action {
	identity := fmt.Sprintf("fix:cr:%s:%s:branch", taskID, headSHA)
	return effect.Action{
		Key:         effect.Key(identity),
		Resource:    crEffectResource(pr, tid),
		Payload:     []byte(identity + ":" + branch + "->" + pr.HeadBranch),
		RetrySafety: effect.RetrySafetyUnsafe,
	}
}
