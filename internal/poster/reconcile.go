// reconcile.go —— 对账用评论查询：按任务身份在 PR 评论里找报告头标记。
//
// 对账的「已生效」证据必须同时满足三条，缺一不可：
//   - 严格报告头（reportHeaderRe：字段齐全、独占一行，宽 Contains 会被正文里
//     引用的 marker 误命中）；
//   - task_id 与 head 精确相等（同任务别的 head 是新旧两轮，不能互证）；
//   - 作者是 bot（配置了 BotUsername 时）——第三方评论可以伪造 marker，若不验
//     作者，攻击者一条评论就能把 unknown 洗成「已生效」。
//
// `argus:start` 是“正在审查”公告 marker，与总结评论是否落库无关，不参与判定。
package poster

import (
	"context"
	"fmt"
	"time"

	"github.com/yi-nology/go-git-platform/model"
)

// reconcileQueryTimeout 对账查询的时间上限：对账挂死会阻塞 WorkerManager 的
// 周期任务，宁可本轮判「判据不足」下轮再查，也不能无限等平台。
const reconcileQueryTimeout = 30 * time.Second

// FindTaskComment 返回携带指定任务报告头的**最后一条**评论 ID（列表升序，
// 最后一条最新）。found=false 且 err=nil 表示列表查询成功但没有匹配标记
// （显式「未生效」证据）。
func (p *Poster) FindTaskComment(ctx context.Context, platform, owner, repo, number, taskID, headSHA string) (int64, bool, error) {
	if p == nil || p.SDK == nil {
		return 0, false, fmt.Errorf("poster: SDK 未装配，无法查询任务评论")
	}
	if taskID == "" {
		return 0, false, fmt.Errorf("poster: task_id 为空，无法匹配报告评论")
	}
	qctx, cancel := context.WithTimeout(ctx, reconcileQueryTimeout)
	defer cancel()
	comments, err := p.SDK.ListIssueComments(qctx, platform, owner, repo, number)
	if err != nil {
		return 0, false, fmt.Errorf("poster: 查询评论列表失败: %w", err)
	}
	var found int64
	for _, c := range comments {
		if c == nil || !p.reportHeaderOwnedByBot(c.Author) {
			continue
		}
		m := reportHeaderRe.FindStringSubmatch(c.Body)
		if len(m) != 3 || m[1] != taskID || m[2] != headSHA {
			continue
		}
		found = c.ID // 列表升序：保留最后一个匹配（最新）
	}
	return found, found > 0, nil
}

// reportHeaderOwnedByBot 作者校验：未配置 BotUsername 时不校验（历史部署兼容），
// 配置后缺作者/他人作者一律不认。
func (p *Poster) reportHeaderOwnedByBot(author *model.CRUser) bool {
	if p.BotUsername == "" {
		return true
	}
	return author != nil && author.Username == p.BotUsername
}
