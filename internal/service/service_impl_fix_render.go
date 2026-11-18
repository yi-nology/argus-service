// service_impl_fix_render.go —— Service 方法体的包级实现（转发壳见原文件）。

package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// fixAgentTimeout 的包级实现（转发壳见原文件）。
func fixAgentTimeout(s *Service) time.Duration {
	if s.Cfg.FixTimeout > 0 {
		return s.Cfg.FixTimeout
	}
	return fixDefaultTimout
}

// serviceFixFinding 的包级实现（转发壳见原文件）。
// #92：写侧 admin 门（对照 suppress/restore 同口径）——ci 档 token 触发
// 沙箱修复+push 是越权面：自动修复改写远端代码，权限档不得低于人工抑制。
func serviceFixFinding(s *Service, ctx context.Context, platform, owner, repo, number, fp string) (map[string]any, error) {
	if ScopeFromContext(ctx) != "admin" {
		return nil, errors.New("fix_finding 需 admin 档 token（自动修复为写侧操作）")
	}
	if !s.repoAllowedFor(ctx, platform, owner, repo) {
		return nil, errors.New(errRepoDenied)
	}
	fp = strings.ToLower(strings.TrimSpace(fp))
	if len(fp) < 6 {
		return nil, fmt.Errorf("fp 须为报告中的指纹短码（≥6 位十六进制）")
	}
	key := platform + "/" + owner + "/" + repo + "#" + number + "/" + fp
	if _, busy := s.fixInflight.LoadOrStore(key, true); busy {
		return nil, fmt.Errorf("同一条 finding 的修复已在执行中，请稍后查询")
	}
	defer s.fixInflight.Delete(key)
	ctx, cancel := context.WithTimeout(ctx, s.fixAgentTimeout())
	defer cancel()
	res, ff, err := s.fixExec(ctx, platform, owner, repo, number, fp)
	if err != nil {
		s.audit(ctx, auditFindingFixDone, "platform", platform, "owner", owner, "repo", repo,
			"number", number, "fingerprint", fp, "ok", false, "channel", "mcp", "error", err.Error())
		return nil, err
	}
	s.audit(ctx, auditFindingFixDone, "platform", platform, "owner", owner, "repo", repo,
		"number", number, "fingerprint", fp, "ok", res.Verified, "channel", "mcp", "agent", res.AgentName)
	return fixResultMap(*ff, res), nil
}
