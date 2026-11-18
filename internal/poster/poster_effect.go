// poster_effect.go —— 创建型副作用的接线助手（C1）：pr 资源标识、评论结果 JSON、
// 幂等键/身份载荷统一构造、带闸的评论创建。按 ID 的 update 不走闸。
package poster

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/yi-nology/argus/internal/effect"
	"github.com/yi-nology/argus/internal/plugin"
	"github.com/yi-nology/argus/internal/tenant"
)

// effectRunner 惰性装配副作用闸（bootstrap 不感知本细节）：显式注入优先，
// 否则从 Store 构造。Store 为 nil 时 runner 走 fail-soft 直通。
func (p *Poster) effectRunner() *effect.Runner {
	if p.Effects != nil {
		return p.Effects
	}
	return effect.NewFromStore(p.Store, effect.DefaultTTL, p.Log, effect.Enabled())
}

// prEffectResource 评论作用的资源：同 PR 的评论创建互斥（跨任务也互斥）。
// 租户前缀（#83）：同坐标 PR 跨租户不再共享评论闸（A 的评论创建不会被 B
// 判成 Replayed）；legacy 无前缀资源 = default 租户（存量行兼容）。
func prEffectResource(pr plugin.PRContext, tid string) string {
	return tenant.Key(tid, "pr:"+pr.Platform+":"+pr.Owner+"/"+pr.Repo+"#"+pr.Number)
}

// commentEffectResult 评论创建结果载荷（Replayed 时从这里取回 comment_id）。
type commentEffectResult struct {
	CommentID int64 `json:"comment_id"`
}

// encodeCommentID 把新评论 ID 落成 Result.Payload。
func encodeCommentID(id int64) effect.Result {
	raw, err := json.Marshal(commentEffectResult{CommentID: id})
	if err != nil { // int64 不可能失败；保守返回空载荷由解码侧报错
		return effect.Result{}
	}
	return effect.Result{Payload: raw}
}

// decodeCommentID 从既有 Result 取回 comment_id（空/坏 JSON/非正 ID = 显式失败）。
func decodeCommentID(res effect.Result) (int64, error) {
	var out commentEffectResult
	if len(res.Payload) == 0 {
		return 0, fmt.Errorf("poster: 副作用结果为空，无法取回 comment_id")
	}
	if err := json.Unmarshal(res.Payload, &out); err != nil {
		return 0, fmt.Errorf("poster: 副作用结果不是合法 JSON: %w", err)
	}
	if out.CommentID <= 0 {
		return 0, fmt.Errorf("poster: 副作用结果缺 comment_id")
	}
	return out.CommentID, nil
}

// commentEffectAction 构造评论创建动作：Key 声明「同一逻辑动作」，Payload 只放
// **稳定身份**，不放报告正文——正文跨重试可能重新生成，放进身份会把正常重试
// 误判成 ErrActionConflict。RetrySafetyUnsafe：平台创建评论没有幂等键。
func commentEffectAction(identity string, pr plugin.PRContext, tid string) effect.Action {
	return effect.Action{
		Key:         effect.Key(identity),
		Resource:    prEffectResource(pr, tid),
		Payload:     []byte(identity),
		RetrySafety: effect.RetrySafetyUnsafe,
	}
}

// createCommentWithEffect 新建评论（至多一次）。taskID 为空（历史测试辅助入口）
// 时直通，不登记副作用；Replayed 取回既有 comment_id，不重复创建。
func (p *Poster) createCommentWithEffect(ctx context.Context, taskID, identity string,
	pr plugin.PRContext, body string) (int64, error) {

	create := func(ctx context.Context) (int64, error) {
		c, err := p.SDK.CreateIssueComment(ctx, pr.Platform, pr.Owner, pr.Repo, pr.Number, body)
		if err != nil {
			return 0, err
		}
		return c.ID, nil
	}
	if taskID == "" {
		return create(ctx)
	}
	outcome, res, err := p.effectRunner().Do(ctx, commentEffectAction(identity, pr, tenant.FromContext(ctx)),
		func(ctx context.Context, _ effect.Lease) (effect.Result, error) {
			id, err := create(ctx)
			if err != nil {
				return effect.Result{}, err
			}
			return encodeCommentID(id), nil
		})
	if err != nil {
		return 0, err
	}
	id, err := decodeCommentID(res)
	if err != nil {
		return 0, err
	}
	if outcome == effect.OutcomeReplayed {
		p.Log.Info("argus.poster.comment_effect_replayed", "task", taskID, "comment_id", id)
	}
	return id, nil
}
