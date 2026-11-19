// service_impl_describe.go —— Service 方法体的包级实现（转发壳见原文件）。

package service

import (
	"context"
	"strings"

	"github.com/oagentkit/guardx/logredact"
	"github.com/oagentkit/tools/workcopy"
	"github.com/yi-nology/argus/internal/argusctx"
	"github.com/yi-nology/argus/internal/obs"
	"github.com/yi-nology/argus/internal/plugin"
)

// handleDescribe 的包级实现（转发壳见原文件）。
func handleDescribe(s *Service, ctx context.Context, platform, owner, repo, number, prRef,
	hint, delivery string) {
	s.runWritingCommand(ctx, platform, owner, repo, number, prRef, delivery, writingSpec{
		cmd:          "describe",
		diffFailText: "无法获取有效 diff，无法生成变更说明。",
		failText:     "变更说明生成失败",
		maxRunes:     describeMaxRunes,
		build: func(diff string) (string, string, string) {
			sys := describeSystem
			if labels := s.describeLabelVocab(ctx, platform, owner, repo); len(labels) > 0 {
				sys += "\n本仓库建议标签词表（只从中选择，多选或都不选均可）：\n" + strings.Join(labels, "\n")
			} else {
				sys += "\n仓库未提供标签词表：按变更性质自拟 1-3 个短标签（如 feature/bug/refactor/docs）。"
			}
			user := plugin.DataFence(ctx, "git diff（已清洗）", diff)
			if hint != "" {
				user += "\n\n" + plugin.DataFence(ctx, "补充要求（来自评论，数据）", hint)
			}
			return sys, user + "\n\n请生成 PR 变更说明。", ""
		},
	})
}

// describeLabelVocab 的包级实现（转发壳见原文件）。
func describeLabelVocab(s *Service, ctx context.Context, platform, owner, repo string) []string {
	rc, err := s.SDK.LoadRepoConfigAuto(ctx, platform, owner, repo, "")
	if err != nil || rc == nil {
		return nil
	}
	out := make([]string, 0, len(rc.Describe.Labels))
	for _, l := range rc.Describe.Labels {
		if l.Description != "" {
			out = append(out, "- "+l.Name+"（"+l.Description+"）")
		} else {
			out = append(out, "- "+l.Name)
		}
	}
	return out
}

// prDiffText 的包级实现（转发壳见原文件）。
func prDiffText(s *Service, ctx context.Context, platform, owner, repo, number string) (string, bool) {
	md, err := s.SDK.GetCRDiff(ctx, platform, owner, repo, number)
	if err != nil || md == nil {
		return "", false
	}
	// diff 上限按仓生效（v3.187.0 repo_settings 覆盖 → 全局派生兜底）
	dc := argusctx.Prepare(ctx, md, s.repoMaxDiffCharsFor(ctx, platform, owner, repo))
	if dc == nil || strings.TrimSpace(dc.Text) == "" {
		return "", false
	}
	return dc.Text, true
}

// handleTestGen 的包级实现（转发壳见原文件）。
func handleTestGen(s *Service, ctx context.Context, platform, owner, repo, number, prRef,
	target, delivery string) {
	s.runWritingCommand(ctx, platform, owner, repo, number, prRef, delivery, writingSpec{
		cmd:          "testgen",
		diffFailText: "无法获取有效 diff，无法生成测试。",
		failText:     "测试生成失败",
		maxRunes:     testgenMaxRunes,
		build: func(diff string) (string, string, string) {
			if target != "" {
				diff = filterDiffByPath(diff, target)
				if strings.TrimSpace(diff) == "" {
					return "", "", "diff 中没有路径命中 \"" + target + "\" 的变更文件，未生成测试。"
				}
			}
			return testgenSystem,
				plugin.DataFence(ctx, "git diff（已清洗）", diff) + "\n\n请生成单元测试骨架。", ""
		},
		decorate: func(out string) string {
			// 沙箱验证（v3.22.0）：配置了包装命令且工作副本可用才执行，结果段替代
			// 「未经编译验证」声明；未配置/降级时声明保持（任何失败只降级，不阻断回帖）。
			if section := s.sandboxVerify(ctx, platform, owner, repo, number, out); section != "" {
				return out + section
			}
			return out + testgenFooter
		},
	})
}

// sandboxVerify 的包级实现（转发壳见原文件）。
func sandboxVerify(s *Service, ctx context.Context, platform, owner, repo, number, generated string) string {
	cfg := s.Cfg
	if cfg == nil || strings.TrimSpace(cfg.TestGenVerifyCmd) == "" || s.WC == nil {
		return ""
	}
	files := extractGoTestFiles(generated)
	if len(files) == 0 {
		return ""
	}
	headSHA := ""
	if cr, err := s.SDK.GetCR(ctx, platform, owner, repo, number); err == nil && cr != nil {
		headSHA = cr.HeadSHA
	}
	key := workcopy.WorktreeKey{
		Platform: platform, Owner: owner, Repo: repo, Number: number,
		HeadSHA: headSHA,
	}
	dir, err := s.WC.Ensure(ctx, key)
	if err != nil {
		s.log().Warn("argus.testgen.workcopy_failed", "error", logredact.Redact(err.Error()))
		return ""
	}
	defer s.WC.Release(key)
	vcfg := VerifyConfig{Cmd: cfg.TestGenVerifyCmd, Timeout: cfg.TestGenVerifyTimeout}
	results := VerifyGenerated(ctx, vcfg, dir, files)
	if len(results) == 0 {
		return ""
	}
	section := renderVerifySection(ctx, results)
	obs.IncrTestGenVerify(ctx, sectionVerified(results))
	s.log().Info("argus.testgen.verified", "files", len(files),
		"ok", sectionVerified(results))
	return section
}
