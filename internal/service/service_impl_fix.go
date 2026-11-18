// service_impl_fix.go —— Service 方法体的包级实现（转发壳见原文件）。

package service

import (
	"context"
	"fmt"
	"time"

	"github.com/oagentkit/guardx/logredact"
	"github.com/oagentkit/tools/workcopy"
	"github.com/oekit/concurrency/async"
	"github.com/yi-nology/argus/internal/commentcmd"
	"github.com/yi-nology/argus/internal/plugin"
)

// handleFix 的包级实现（转发壳见原文件）。
func handleFix(s *Service, ctx context.Context, platform, owner, repo, prNum, prRef, fp, delivery string) {
	if s.Cfg.FixAgent == "" {
		s.postQAError(ctx, platform, owner, repo, prNum,
			"修复功能未启用：需服务级配置 ARGUS_FIX_AGENT（建议同时配 ARGUS_FIX_VERIFY_CMD 验证环）。")
		return
	}
	if s.WC == nil {
		s.postQAError(ctx, platform, owner, repo, prNum, "修复不可用：服务未配置工作副本池。")
		return
	}
	rec, ok := s.Store.ResolvePrintRecord(ctx, platform, owner, repo, prNum, fp)
	if !ok {
		s.postQAError(ctx, platform, owner, repo, prNum,
			"未找到匹配的 finding（或短码有歧义），请核对报告中 `fp:` 开头的短码后重试。")
		return
	}
	if rec.File == "" {
		s.postQAError(ctx, platform, owner, repo, prNum, "该 finding 未关联具体文件，无法自动修复。")
		return
	}

	key := platform + "/" + owner + "/" + repo + "#" + prNum + "/" + fp
	if _, busy := s.fixInflight.LoadOrStore(key, true); busy {
		s.postQAError(ctx, platform, owner, repo, prNum, "同一条 finding 的修复已在执行中，请等它完成后回帖。")
		return
	}
	s.audit(ctx, auditFindingFixReq, "platform", platform, "owner", owner, "repo", repo,
		"number", prNum, "fingerprint", fp, "agent", s.Cfg.FixAgent)
	s.postQAError(ctx, platform, owner, repo, prNum,
		fmt.Sprintf("修复任务已受理（fp:%s，agent %s）——异步执行中，完成后回帖结果。\n\n<!-- argus:fix delivery=%s phase=accepted -->",
			shortFP(fp), s.Cfg.FixAgent, delivery))
	// async.GoSafe（v5.4.2）：runFixReply 跑完整 LLM agent + 沙箱链路，裸
	// goroutine 期 panic 即进程死（215 曾炸出 guardPanic 卡死教训）——panic
	// 转结构化留痕，fixInflight 键随进程清理兜底。
	async.GoSafe(func() {
		s.runFixReply(key, platform, owner, repo, prNum, fp, delivery)
	})
}

// fixExec 的包级实现（转发壳见原文件）。
func fixExec(s *Service, ctx context.Context, platform, owner, repo, number, fp string) (*fixResult, *fixFinding, error) {
	if s.Cfg == nil || s.Cfg.FixAgent == "" {
		return nil, nil, fmt.Errorf("修复功能未启用（ARGUS_FIX_AGENT 未配置）")
	}
	if s.WC == nil {
		return nil, nil, fmt.Errorf("修复不可用：服务未配置工作副本池")
	}
	rec, ok := s.Store.ResolvePrintRecord(ctx, platform, owner, repo, number, fp)
	if !ok {
		return nil, nil, fmt.Errorf("未找到匹配的 finding（fp:%s），请核对报告短码", fp)
	}
	if rec.File == "" {
		return nil, nil, fmt.Errorf("该 finding 未关联具体文件，无法自动修复")
	}
	ff := &fixFinding{File: rec.File, Line: rec.Line, Severity: rec.Severity, Comment: rec.Comment}
	// head SHA 取不到回落默认分支（降级可修复，摘录行号可能漂移，agent 以意见上下文为准）
	headSHA := ""
	if cr, err := s.SDK.GetCR(ctx, platform, owner, repo, number); err == nil && cr != nil {
		headSHA = cr.HeadSHA
	}
	wcKey := workcopy.WorktreeKey{Platform: platform, Owner: owner, Repo: repo, Number: number, HeadSHA: headSHA}
	dir, err := s.WC.Ensure(ctx, wcKey)
	if err != nil {
		return nil, nil, fmt.Errorf("工作副本不可用: %w", err)
	}
	defer s.WC.Release(wcKey)
	res, err := runFixSandbox(ctx, fixRunConfig{
		SrcDir:    dir,
		WorkRoot:  s.Cfg.WorkRoot,
		AgentName: s.Cfg.FixAgent,
		Verify:    VerifyConfig{Cmd: s.Cfg.FixVerifyCmd, Timeout: s.Cfg.FixVerifyTimeout},
		Finding:   *ff,
		Timeout:   s.fixAgentTimeout(),
		Log:       s.log(),
	})
	if err != nil {
		return nil, nil, err
	}
	return res, ff, nil
}

// runFixReply 的包级实现（转发壳见原文件）。
func runFixReply(s *Service, key, platform, owner, repo, prNum, fp, delivery string) {
	defer s.fixInflight.Delete(key)
	ctx, cancel := context.WithTimeout(context.Background(), s.fixAgentTimeout())
	defer cancel()

	res, ff, err := s.fixExec(ctx, platform, owner, repo, prNum, fp)
	pctx, pcancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer pcancel()
	if err != nil {
		s.log().Error("argus.fix.failed", "pr", fmt.Sprintf("%s/%s/%s#%s", platform, owner, repo, prNum),
			"fp", shortFP(fp), "error", logredact.Redact(err.Error()))
		s.audit(ctx, auditFindingFixDone, "platform", platform, "owner", owner, "repo", repo,
			"number", prNum, "fingerprint", fp, "ok", false, "error", err.Error())
		s.postQAError(pctx, platform, owner, repo, prNum,
			fmt.Sprintf("修复执行失败（fp:%s）：%s\n\n<!-- argus:fix delivery=%s phase=done -->",
				shortFP(fp), logredact.Redact(err.Error()), delivery))
		return
	}
	s.audit(ctx, auditFindingFixDone, "platform", platform, "owner", owner, "repo", repo,
		"number", prNum, "fingerprint", fp, "ok", res.Verified, "agent", res.AgentName)
	s.postQAError(pctx, platform, owner, repo, prNum, renderFixReply(fp, *ff, res, delivery))
}

// dispatchRecheck 的包级实现（转发壳见原文件）。
func dispatchRecheck(s *Service, ctx context.Context, question, platform, owner, repo,
	prNum, prRef, delivery string) bool {
	fp, note, kind := commentcmd.ParseRecheck(question)
	switch kind {
	case commentcmd.RecheckFP:
		s.handleRecheckFinding(ctx, platform, owner, repo, prNum, prRef, fp, note, delivery)
		return true
	case commentcmd.RecheckFPBad:
		s.postQAError(ctx, platform, owner, repo, prNum,
			"复核指令的 fp 短码格式不对（需 `fp:` + 6-64 位十六进制），请核对报告中 `fp:` 开头的短码后重试。")
		return true
	}
	return false
}

// handleRecheckFinding 的包级实现（转发壳见原文件）。
func handleRecheckFinding(s *Service, ctx context.Context, platform, owner, repo,
	prNum, prRef, fp, note, delivery string) {
	rec, ok := s.Store.ResolvePrintRecord(ctx, platform, owner, repo, prNum, fp)
	if !ok {
		s.postQAError(ctx, platform, owner, repo, prNum,
			"未找到匹配的意见（或短码有歧义），请核对报告中 `fp:` 开头的短码后重试。")
		return
	}
	s.runWritingCommand(ctx, platform, owner, repo, prNum, prRef, delivery, writingSpec{
		cmd:          "recheck",
		diffFailText: "无法获取有效 diff，无法复核该意见。",
		failText:     "意见复核失败",
		maxRunes:     recheckMaxRunes,
		build: func(diff string) (string, string, string) {
			target := fmt.Sprintf("fp 短码: %s\nfile: %s\nline: %d\nseverity: %s\n意见: %s",
				shortFP(rec.Print), rec.File, rec.Line, rec.Severity, rec.Comment)
			user := plugin.DataFence("git diff（已清洗）", diff)
			user += "\n\n" + plugin.DataFence("待复核意见（数据）", target)
			if note != "" {
				user += "\n\n" + plugin.DataFence("补充上下文（来自评论，数据）", note)
			}
			return recheckSystem, user + "\n\n请复核该意见。", ""
		},
	})
}
