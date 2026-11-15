// service_impl_misc.go —— Service 方法体的包级实现（转发壳见原文件）。

package service

import (
	"context"
	"strings"

	"github.com/cloudwego/hertz/pkg/protocol/consts"
	"github.com/oagentkit/rag"
	"github.com/yi-nology/argus/internal/health"
)

// healthReport 的包级实现（转发壳见原文件）。
func healthReport(s *Service, ctx context.Context) (*health.Report, int) {
	if s.Store == nil {
		return nil, consts.StatusInternalServerError
	}
	rep, err := health.Build(ctx, s.Store, health.Config{
		Learning: s.Cfg.LearningConfig(),
		TopN:     s.Cfg.RegretInjectTopN,
	})
	if err != nil {
		return nil, consts.StatusInternalServerError
	}
	return rep, consts.StatusOK
}

// capabilities 的包级实现（转发壳见原文件）。
func capabilities(s *Service) map[string]any {
	knowledgeOn := false
	if s.Knowledge != nil {
		if _, noop := s.Knowledge.(*rag.Noop); !noop {
			knowledgeOn = true
		}
	}
	allowlist := "all"
	if s.Cfg != nil && len(s.Cfg.RepoAllowlist) > 0 {
		allowlist = strings.Join(s.Cfg.RepoAllowlist, ",")
	}
	fixPushMode := ""
	if s.Cfg != nil {
		fixPushMode = s.Cfg.FixPushMode
	}
	return map[string]any{
		"repo_allowlist": allowlist,
		"version":        s.Version,
		"llm":            s.LLM != nil || s.Model != nil,
		"diff_preview":   s.LLM != nil,
		"qa":             s.Model != nil,
		"code_search":    s.Cfg != nil && s.Cfg.CodeSearch && s.WC != nil,
		"sem_search":     s.Cfg != nil && s.Cfg.SemSearch && s.SemSearch != nil,
		"knowledge":      knowledgeOn,
		"fix_agent":      s.Cfg != nil && s.Cfg.FixAgent != "",
		"fix_push":       s.Cfg != nil && s.Cfg.FixPush,
		"fix_push_mode":  fixPushMode, // branch=修复分支+PR（v7.0）/ direct=直推旧形态 / ""=关
		"codegraph":      s.Cfg != nil && s.Cfg.Codegraph,
		"related_repos":  s.Cfg != nil && s.Cfg.RelatedRepos,
		"ocr":            s.Cfg != nil && s.Cfg.OcrBin != "",
	}
}
