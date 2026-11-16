// comment_resolver.go —— 评论型未知态的判定：用平台评论里的报告头标记
// （`argus:report task_id=<id> `）确认动作是否已生效。
//
// 判定边界：
//   - 只认 `argus:report` 报告头，**不认** `argus:start`（start marker 是
//     “正在审查”公告，和总结评论是否落库无关，拿来判定会误报已生效）；
//   - 平台查询成功且无标记 = 显式的「未生效」证据：评论创建动作声明为
//     RetrySafetyUnsafe（平台无幂等键），故要带 ForceRetryable 签字放行；
//   - 未知前缀/坏 Key/坏 Resource 返回 ok=false（跳过并留痕，不改状态）；
//   - 平台查询失败返回 error（判据不足，本轮不裁决，下一轮再查）。
package effect

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/yi-nology/goagent-kernel/sideeffect"

	"github.com/yi-nology/argus/internal/storage"
)

// CommentFinder 按任务身份查平台评论（poster.FindTaskComment 的接口投影）。
type CommentFinder interface {
	FindTaskComment(ctx context.Context, platform, owner, repo, number, taskID, headSHA string) (int64, bool, error)
}

// CommentResolver 评论型 Key 的对账裁决器。
type CommentResolver struct {
	finder CommentFinder
}

// NewCommentResolver 组装评论裁决器。finder 为 nil 时所有 Key 都跳过
// （ok=false），不会把「没装查询能力」误判成「评论不存在」。
func NewCommentResolver(f CommentFinder) *CommentResolver {
	return &CommentResolver{finder: f}
}

// commentResolutionPayload 已生效时的结果载荷（与 poster 的 comment_id 载荷同形）。
type commentResolutionPayload struct {
	CommentID int64 `json:"comment_id"`
}

// commentKey 解析后的评论型副作用 Key。
type commentKey struct {
	kind    string // summary / rotate
	taskID  string
	headSHA string
	oldID   int64 // rotate 专用
}

// parseCommentKey 解析 Key。合法形态（段数精确匹配，任务 ID/head 不含冒号）：
//
//	comment:summary:<taskID>:<headSHA>
//	comment:rotate:<taskID>:<headSHA>:<oldID>
//
// headSHA 允许为空（手动任务回填失败时报告头 head=）；oldID 必须是正整数。
func parseCommentKey(key string) (commentKey, bool) {
	parts := strings.Split(key, ":")
	if len(parts) < 4 || parts[0] != "comment" || parts[2] == "" {
		return commentKey{}, false
	}
	switch parts[1] {
	case "summary":
		if len(parts) != 4 {
			return commentKey{}, false
		}
		return commentKey{kind: "summary", taskID: parts[2], headSHA: parts[3]}, true
	case "rotate":
		if len(parts) != 5 {
			return commentKey{}, false
		}
		oldID, err := strconv.ParseInt(parts[4], 10, 64)
		if err != nil || oldID <= 0 {
			return commentKey{}, false
		}
		return commentKey{kind: "rotate", taskID: parts[2], headSHA: parts[3], oldID: oldID}, true
	default:
		return commentKey{}, false
	}
}

// parsePRResource 解析 Resource：
//
//	pr:<platform>:<owner>/<repo>#<number>
//
// owner/repo 部分允许包含 `/`（GitLab 子组），以最后一个 `/` 分段：
// 前者归 owner（API 的 group/subgroup 语义），最后一段归 repo。
func parsePRResource(resource string) (platform, owner, repo, number string, ok bool) {
	const prefix = "pr:"
	if !strings.HasPrefix(resource, prefix) {
		return "", "", "", "", false
	}
	rest := strings.TrimPrefix(resource, prefix)
	i := strings.Index(rest, ":")
	if i <= 0 {
		return "", "", "", "", false
	}
	platform = rest[:i]
	tail := rest[i+1:]
	j := strings.LastIndex(tail, "#")
	if j <= 0 || j == len(tail)-1 {
		return "", "", "", "", false
	}
	repoPath, number := tail[:j], tail[j+1:]
	k := strings.LastIndex(repoPath, "/")
	if k <= 0 || k == len(repoPath)-1 {
		return "", "", "", "", false
	}
	return platform, repoPath[:k], repoPath[k+1:], number, true
}

// Resolve 判定一条未知态 Key。第二个返回值表示「这个 Key 类型是否受支持」：
// false = 跳过（未知前缀/坏 Key/坏 Resource），不改变状态。
func (r *CommentResolver) Resolve(ctx context.Context, u storage.UncertainSideEffect) (sideeffect.Resolution, bool, error) {
	if r == nil || r.finder == nil {
		return sideeffect.Resolution{}, false, nil
	}
	ck, ok := parseCommentKey(u.Key)
	if !ok {
		return sideeffect.Resolution{}, false, nil
	}
	platform, owner, repo, number, ok := parsePRResource(u.Resource)
	if !ok {
		return sideeffect.Resolution{}, false, nil
	}
	id, found, err := r.finder.FindTaskComment(ctx, platform, owner, repo, number, ck.taskID, ck.headSHA)
	if err != nil {
		return sideeffect.Resolution{}, true, fmt.Errorf("effect: 查询任务评论标记失败: %w", err)
	}
	if found {
		if id <= 0 {
			return sideeffect.Resolution{}, true, fmt.Errorf("effect: 评论标记命中但 comment_id 非法: %d", id)
		}
		payload, merr := json.Marshal(commentResolutionPayload{CommentID: id})
		if merr != nil { // int64 不可能失败；保守按查询失败处理
			return sideeffect.Resolution{}, true, fmt.Errorf("effect: 编码 comment_id 失败: %w", merr)
		}
		return sideeffect.Resolution{
			Applied: true,
			Result:  sideeffect.Result{Payload: payload},
			Reason:  "comment marker found",
		}, true, nil
	}
	// 平台查询成功且无标记：动作未生效。评论创建无平台幂等键（Unsafe），
	// 这里是明确证据，显式签字允许重试。
	return sideeffect.Resolution{
		Applied:        false,
		ForceRetryable: true,
		Reason:         "comment marker absent",
	}, true, nil
}
