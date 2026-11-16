package argusctx

import (
	"context"
	"fmt"
	"strconv"

	"github.com/yi-nology/go-git-platform/model"
	"github.com/yi-nology/go-git-platform/provider"
)

// listIssueComments 实现 SDKCtx.ListIssueComments（评论读取）。
func listIssueComments(s *SDKCtx, ctx context.Context, platform, owner, repo, number string) ([]*model.IssueComment, error) {
	if c, err := callIface(s, ctx, platform, "NoteManager",
		func(ctx context.Context, nm provider.NoteManager) ([]*model.IssueComment, error) {
			return nm.ListNotes(ctx, owner, repo, number)
		}); err == nil {
		return c, nil
	}
	return callIface(s, ctx, platform, "IssueManager",
		func(ctx context.Context, im provider.IssueManager) ([]*model.IssueComment, error) {
			return im.ListIssueComments(ctx, owner, repo, number)
		})
}

// createIssueComment 实现 SDKCtx.CreateIssueComment（评论创建）。
func createIssueComment(s *SDKCtx, ctx context.Context, platform, owner, repo, number, body string) (*model.IssueComment, error) {
	body = maskOutboundPII(body) // v6.16.0 出站值面 PII 脱敏（choke point 单点）
	return call(s, ctx, platform, func(ctx context.Context, p provider.Provider) (*model.IssueComment, error) {
		// 优先走 DiffManager.CreateNote（MR Notes API），兼容所有 CR 场景。
		if dm, ok := p.(provider.DiffManager); ok {
			noteID, err := dm.CreateNote(ctx, owner, repo, number, body)
			if err == nil {
				// 解析失败留痕（深挖修复）：Comment.ID=0 会破坏按评论 ID 幂等更新的锚点
				id, perr := strconv.ParseInt(noteID, 10, 64)
				if perr != nil {
					s.log.Warn("argus.sdkctx.note_id_unparsable", "platform", platform,
						"note_id", noteID, "error", perr.Error())
				}
				return &model.IssueComment{ID: id, Body: body}, nil
			}
			s.log.Warn("argus.sdkctx.create_note_fallback", "platform", platform, "error", err.Error())
		}
		// 回退到 IssueManager（Issue 评论 API）
		im, ok := p.(provider.IssueManager)
		if !ok {
			return nil, fmt.Errorf("sdkctx: 平台 %s 不支持 DiffManager 也不支持 IssueManager", platform)
		}
		return im.CreateIssueComment(ctx, owner, repo, number, body)
	})
}

// addIssueCommentReaction 实现 SDKCtx.AddIssueCommentReaction（评论表情回应）。
func addIssueCommentReaction(s *SDKCtx, ctx context.Context, platform, owner, repo string,
	commentID int64, emoji string) error {
	_, err := callIface(s, ctx, platform, "ReactionManager",
		func(ctx context.Context, rm provider.ReactionManager) (*model.Reaction, error) {
			return rm.AddIssueCommentReaction(ctx, owner, repo, commentID, emoji)
		})
	return err
}

// updateIssueComment 实现 SDKCtx.UpdateIssueComment（评论更新路由）。
func updateIssueComment(s *SDKCtx, ctx context.Context, platform, owner, repo, number string, commentID int64, body string) (*model.IssueComment, error) {
	body = maskOutboundPII(body) // v6.16.0 出站值面 PII 脱敏（choke point 单点）
	if c, err := callIface(s, ctx, platform, "NoteManager",
		func(ctx context.Context, nm provider.NoteManager) (*model.IssueComment, error) {
			return nm.UpdateNote(ctx, owner, repo, number, strconv.FormatInt(commentID, 10), body)
		}); err == nil {
		return c, nil
	}
	c, err := callIface(s, ctx, platform, "IssueManager",
		func(ctx context.Context, im provider.IssueManager) (*model.IssueComment, error) {
			return im.UpdateIssueComment(ctx, owner, repo, number, commentID, body)
		})
	if err == nil {
		return c, nil
	}
	fallback, ferr := call(s, ctx, platform, func(ctx context.Context, p provider.Provider) (*model.IssueComment, error) {
		dm, ok := p.(provider.DiffManager)
		if !ok {
			return nil, err
		}
		nid, cerr := dm.CreateNote(ctx, owner, repo, number, body)
		if cerr != nil {
			s.log.Warn("argus.sdkctx.update_swap_create_failed", "platform", platform, "error", cerr.Error())
			return nil, err
		}
		newID, perr := strconv.ParseInt(nid, 10, 64)
		if perr != nil {
			// 新评论已建但 ID 不可解析：删旧保新（缓存定位失效属可容忍降级）
			s.log.Warn("argus.sdkctx.note_id_unparsable", "platform", platform, "note_id", nid, "error", perr.Error())
			newID = 0
		}
		if derr := dm.DeleteNote(ctx, owner, repo, number, strconv.FormatInt(commentID, 10)); derr != nil {
			s.log.Warn("argus.sdkctx.update_swap_delete_failed", "platform", platform,
				"comment_id", commentID, "error", derr.Error())
		}
		return &model.IssueComment{ID: newID, Body: body}, nil
	})
	if ferr == nil {
		return fallback, nil
	}
	return c, err
}

// deleteIssueComment 实现 SDKCtx.DeleteIssueComment（评论删除面）。
func deleteIssueComment(s *SDKCtx, ctx context.Context, platform, owner, repo, number string, commentID int64) error {
	// 主路：IssueManager.DeleteIssueComment（gp#33 → v0.114.0 收编；GitLab/腾讯
	// 工蜂 route through issue，七平台全覆盖）；DiffManager.DeleteNote 兜底保留
	// （旧版本地板兼容，argus go.mod 已 v0.114 主路恒命中）。
	if _, err := callIface(s, ctx, platform, "IssueManager",
		func(ctx context.Context, im provider.IssueManager) (bool, error) {
			return true, im.DeleteIssueComment(ctx, owner, repo, number, commentID)
		}); err == nil {
		return nil
	}
	_, err := call(s, ctx, platform, func(ctx context.Context, p provider.Provider) (bool, error) {
		dm, ok := p.(provider.DiffManager)
		if !ok {
			return false, fmt.Errorf("platform %s: 无评论删除面（IssueManager/DiffManager 均未实现）", platform)
		}
		if derr := dm.DeleteNote(ctx, owner, repo, number, strconv.FormatInt(commentID, 10)); derr != nil {
			return false, derr
		}
		return true, nil
	})
	return err
}
