// service_impl_cmd.go —— Service 方法体的包级实现（转发壳见原文件）。

package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	datacache "github.com/oekit/data/cache"
	"github.com/yi-nology/argus/internal/commentcmd"
	"github.com/yi-nology/argus/internal/plugin"
	"github.com/yi-nology/argus/internal/storage"
	"github.com/yi-nology/argus/internal/tenant"
	"github.com/yi-nology/go-git-platform/model"
)

// handleChangelog 的包级实现（转发壳见原文件）。
func handleChangelog(s *Service, ctx context.Context, platform, owner, repo, number, prRef,
	hint, delivery string) {
	s.runWritingCommand(ctx, platform, owner, repo, number, prRef, delivery, writingSpec{
		cmd:          "changelog",
		diffFailText: "无法获取有效 diff，无法起草 CHANGELOG 条目。",
		failText:     "CHANGELOG 起草失败",
		maxRunes:     changelogMaxRunes,
		build: func(diff string) (string, string, string) {
			user := plugin.DataFence("git diff（已清洗）", diff)
			if hint != "" {
				user += "\n\n" + plugin.DataFence("补充要求（来自评论，数据）", hint)
			}
			return changelogSystem, user + "\n\n请起草 CHANGELOG 条目。", ""
		},
	})
}

// dispatchInfo 的包级实现（转发壳见原文件）。
func dispatchInfo(s *Service, ctx context.Context, question, platform, owner, repo,
	prNum, prRef, delivery string) bool {
	switch {
	case commentcmd.ExtractHelp(question):
		body := commentcmd.HelpText(s.Cfg != nil && s.Cfg.FixAgent != "") + "\n\n<!-- argus:help delivery=" + delivery + " -->"
		s.postCommentReply(ctx, platform, owner, repo, prNum, prRef, body, "help", len(body))
		return true
	case commentcmd.ExtractStatus(question):
		body := s.statusText(ctx, platform, owner, repo, prNum) + "\n\n<!-- argus:status delivery=" + delivery + " -->"
		s.postCommentReply(ctx, platform, owner, repo, prNum, prRef, body, "status", len(body))
		return true
	}
	return false
}

// statusText 的包级实现（转发壳见原文件）。
func statusText(s *Service, ctx context.Context, platform, owner, repo, prNum string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## 📊 PR #%s 审查状态\n", prNum)
	tasks, err := s.Store.ListTasksByPR(ctx, platform, owner, repo, prNum, 5)
	if err != nil || len(tasks) == 0 {
		b.WriteString("暂无审查任务。`@bot review` 可发起一轮全量评审。")
		return b.String()
	}
	t := tasks[0]
	state := t.Status
	if t.Status == storage.StatusDone && t.Verdict != "" {
		state = storage.StatusDone + " / " + t.Verdict
	}
	fmt.Fprintf(&b, "- 最近任务 `%s`：%s（%s，head `%s`）\n", shortFP(t.ID), state, t.Kind, shortFP(t.HeadSHA))
	if t.Status == storage.StatusDone {
		if fs, ok := taskFindings(t.ResultJSON); ok {
			fmt.Fprintf(&b, "- 结论：%s，%d 条意见\n", orDash(t.Verdict), len(fs))
		}
	} else if t.Status == storage.StatusRunning || t.Status == storage.StatusPending {
		b.WriteString("- 审查进行中，完成后会回帖报告\n")
	} else if t.Status == storage.StatusFailed {
		fmt.Fprintf(&b, "- 失败原因：%s\n", orDash(t.FailReason))
	}
	if len(tasks) > 1 {
		fmt.Fprintf(&b, "- 历史：共 %d 轮\n", len(tasks))
	}
	b.WriteString("\n下一步：`@bot recheck fp:短码` 复核单条 · `@bot review` 重新审查 · `@bot help` 指令手册")
	return b.String()
}

// collabCache 的包级实现（转发壳见原文件）。
func collabCache(s *Service) *datacache.LocalCache[string] {
	s.qaThrottleMu.Lock()
	defer s.qaThrottleMu.Unlock()
	if s.collabCch == nil {
		s.collabCch = datacache.NewLocalCache(datacache.LocalConfig[string]{
			TTL:     5 * time.Minute,
			MaxSize: 200,
		})
	}
	return s.collabCch
}

// ackComment 的包级实现（转发壳见原文件）。
func ackComment(s *Service, ctx context.Context, platform, owner, repo string, commentID int64) {
	if commentID <= 0 {
		return
	}
	if err := s.SDK.AddIssueCommentReaction(ctx, platform, owner, repo, commentID, model.ReactionEyes); err != nil {
		s.log().Warn("argus.comment.ack_failed", "platform", platform,
			"repo", owner+"/"+repo, "comment_id", commentID, "error", err.Error())
	}
}

// actorCanDrive 的包级实现（转发壳见原文件）。
func actorCanDrive(s *Service, ctx context.Context, platform, owner, repo, actor string) bool {
	if s.Cfg == nil || strings.EqualFold(s.Cfg.CommentGate, "open") {
		return true
	}
	if actor == "" {
		return false
	}
	key := tenant.Key(tenant.FromContext(ctx), platform+"/"+owner+"/"+repo)
	joined, _ := s.collabCache().Get(ctx, key)
	if joined == nil {
		list, err := s.SDK.ListCollaborators(ctx, platform, owner, repo)
		if err != nil {
			s.log().Warn("argus.comment.gate_fail_open", "repo", key, "actor", actor, "error", err.Error())
			return true
		}
		names := make([]string, 0, len(list))
		for _, c := range list {
			if c == nil || c.Username == "" {
				continue
			}
			names = append(names, strings.ToLower(c.Username))
		}
		j := "\n" + strings.Join(names, "\n") + "\n"
		_ = s.collabCache().Set(ctx, key, &j)
		joined = &j
	}
	return strings.Contains(*joined, "\n"+strings.ToLower(actor)+"\n")
}
