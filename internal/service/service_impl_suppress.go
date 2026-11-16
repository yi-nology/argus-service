// service_impl_suppress.go —— Service 方法体的包级实现（转发壳见原文件）。

package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	argus "github.com/yi-nology/argus/biz/model/argus"
	"github.com/yi-nology/argus/internal/domain"
	"github.com/yi-nology/argus/internal/storage"
)

// suppressFindingScoped 的包级实现（转发壳见原文件）。
func suppressFindingScoped(s *Service, ctx context.Context, platform, owner, repo, number, fp string) (map[string]any, error) {
	if ScopeFromContext(ctx) != "admin" {
		return nil, fmt.Errorf("suppress_finding 需 admin 档 token（误报抑制是信任边界写操作，scope 透传二次判定），当前档位不足")
	}
	if !s.repoAllowedFor(ctx, platform, owner, repo) {
		return nil, errors.New(errRepoDenied)
	}
	if owner == "" || repo == "" {
		return nil, fmt.Errorf("owner/repo 不能为空")
	}
	if !isPlatformPath(owner) || !isPlatformPath(repo) {
		return nil, fmt.Errorf("owner/repo 含非法字符")
	}
	if !isDigits(number) || len(number) > 10 {
		return nil, fmt.Errorf("number 必须为纯数字 PR 序号")
	}
	if len(fp) < 6 || len(fp) > 64 {
		return nil, fmt.Errorf("fp 须为报告中的指纹短码（6-64 位十六进制）")
	}
	resp, code := s.SuppressFinding(ctx, &argus.SuppressFindingRequest{
		Platform:    platform,
		Owner:       owner,
		Repo:        repo,
		Number:      number,
		Fingerprint: &fp,
	})
	if code != http.StatusOK {
		return nil, fmt.Errorf("抑制失败（HTTP %d）", code)
	}
	return map[string]any{"matched": resp.Matched, "fingerprint": resp.Fingerprint}, nil
}

// restoreFindingScoped 的包级实现（转发壳见原文件）。
func restoreFindingScoped(s *Service, ctx context.Context, platform, owner, repo, number, fp string) (map[string]any, error) {
	if ScopeFromContext(ctx) != "admin" {
		return nil, fmt.Errorf("restore_finding 需 admin 档 token（scope 透传二次判定），当前档位不足")
	}
	if !s.repoAllowedFor(ctx, platform, owner, repo) {
		return nil, errors.New(errRepoDenied)
	}
	if owner == "" || repo == "" {
		return nil, fmt.Errorf("owner/repo 不能为空")
	}
	if !isPlatformPath(owner) || !isPlatformPath(repo) {
		return nil, fmt.Errorf("owner/repo 含非法字符")
	}
	if !isDigits(number) || len(number) > 10 {
		return nil, fmt.Errorf("number 必须为纯数字 PR 序号")
	}
	if len(fp) < 6 || len(fp) > 64 {
		return nil, fmt.Errorf("fp 须为报告中的指纹短码（6-64 位十六进制）")
	}
	matched, err := s.restoreFindingCore(ctx, platform, owner, repo, number, strings.ToLower(fp))
	if err != nil {
		return nil, err
	}
	return map[string]any{"matched": matched}, nil
}

// restoreFindingCore 的包级实现（转发壳见原文件）。
func restoreFindingCore(s *Service, ctx context.Context, platform, owner, repo, number, fpPrefix string) (bool, error) {
	num, err := strconv.Atoi(number)
	if err != nil {
		return false, fmt.Errorf("number 必须为纯数字 PR 序号")
	}
	// 学习环：解析完整行——restore 的 pattern 键必须由入库原始评语派生
	//（固定 "restore:"+短码会让 hex 被剥离后塌缩到垃圾键，v3.8.0 教训）
	var pluginName, fullFP string
	var resolvedFinding domain.Finding
	if s.Cfg != nil && s.Cfg.RegretLearning {
		if rec, ok := s.Store.ResolvePrintRecord(ctx, platform, owner, repo, number, fpPrefix); ok {
			fullFP = rec.Print
			pluginName = rec.Plugin
			resolvedFinding = domain.Finding{
				File: rec.File, Line: rec.Line, Severity: rec.Severity,
				Comment: rec.Comment, By: rec.Plugin,
			}
		}
	}
	matched, err := s.Store.RestorePrint(ctx, platform, owner, repo, num, fpPrefix)
	if err != nil {
		return false, err
	}
	s.Audit.Log(auditFindingRestore, "platform", platform, "owner", owner,
		"repo", repo, "number", number, "fingerprint", fpPrefix, "matched", matched,
		"token_fp", TokenFPFromContext(ctx))
	if matched && s.Cfg != nil && s.Cfg.RegretLearning && fullFP != "" {
		if err := s.Store.PatternRestore(ctx, platform, owner, repo, pluginName,
			resolvedFinding, time.Now()); err != nil {
			s.log().Warn("argus.regret.pattern_restore_failed", "error", err.Error())
		}
	}
	return matched, nil
}

// suppressFinding 的包级实现（转发壳见原文件）。
func suppressFinding(s *Service, ctx context.Context, req *argus.SuppressFindingRequest) (*argus.SuppressFindingResponse, int) {
	platform, _, ok := s.requirePlatform(req.Platform)
	if !ok {
		return nil, http.StatusBadRequest
	}
	// v6.7.0 P0-02：写面同样收口仓库维度——HTTP 直入此前无 repoAllowedFor
	//（MCP 面 SuppressFindingScoped 已接），约束内 admin 才可抑制。
	if !s.Authorize(ctx, req.Platform, req.Owner, req.Repo) {
		authzDeniedMetric(ctx, "/findings/suppress")
		return nil, http.StatusForbidden
	}
	if req.Number == "" {
		return nil, http.StatusBadRequest
	}

	// 两种输入形态（fp 短码直达 / file+原文近重复）解析出目标身份与处置状态，
	// 详见 helper 注释；resp 非 nil 为解析期已定的终态响应。
	tgt, resp, code := s.suppressResolve(ctx, req, platform)
	if resp != nil {
		return resp, code
	}

	// 学习环/知识飞轮/回写统一用入库原始身份（模式一 resolvedFinding；模式二用请求身份）
	ledgerFinding := tgt.resolvedFinding
	if ledgerFinding.Comment == "" && ledgerFinding.File == "" {
		ledgerFinding = domain.Finding{File: tgt.file, Comment: tgt.comment, By: tgt.pluginName}
	}
	if !tgt.suppressDone {
		var err error
		tgt.matched, err = s.Store.SuppressPrint(ctx, platform, req.Owner, req.Repo, req.Number,
			tgt.fp, ledgerFinding, time.Now())
		if err != nil {
			s.log().Error("argus.argus.suppress_failed", "error", err.Error())
			return nil, http.StatusInternalServerError
		}
	}
	if tgt.matched {
		// 知识飞轮（v3.3）：误报结论回流知识库——R3 search_knowledge 命中
		// "本仓库历史误报"即带抑制上下文，同型误报率随使用下降
		s.recordFalsePositiveKnowledge(ctx, platform, req.Owner, req.Repo, req.Number,
			tgt.fp, ledgerFinding.File, ledgerFinding.Comment)
	}
	// 学习环：人工判误报 → PatternSuppress（失败只告警）。
	// 必须用入库原始评语派生 pattern 键——请求评语（评论路径=整条评论原文）
	// 剥离指令词/hex 后塌缩到垃圾键，真实 pattern 永远收不到人工负票（v3.8.0 修复）。
	if s.Cfg != nil && s.Cfg.RegretLearning {
		if err := s.Store.PatternSuppress(ctx, platform, req.Owner, req.Repo, tgt.pluginName,
			ledgerFinding, time.Now()); err != nil {
			s.log().Warn("argus.regret.pattern_suppress_failed", "error", err.Error())
		}
	}
	// 批 E 组织判例桶写侧聚合（失败只告警同纪律）：仓无关 Pattern 投影 upsert
	// ——新模式落 candidate（进 eval-candidates 待审清单，人工确认才参与提示），
	// 既有模式 HitCount++（抑制信号再确认）。org 轴=Owner 不跨组织。
	if err := s.Store.UpsertFPCase(ctx, storage.FPCaseBucket{
		Pattern:     domain.FPCasePattern(platform, req.Owner, ledgerFinding.File, ledgerFinding.Comment),
		Kind:        ledgerFinding.File, // 类目占位=文件形态（投影归一后收敛）
		Org:         req.Owner,
		SampleRepo:  req.Owner + "/" + req.Repo,
		SamplePrint: tgt.fp,
		Comment:     ledgerFinding.Comment,
		HitCount:    1,
	}); err != nil {
		s.log().Warn("argus.fpcase.upsert_failed", "error", err.Error())
	}
	s.log().Info("argus.argus.suppressed", "pr", req.Owner+"/"+req.Repo+"#"+req.Number,
		"file", tgt.file, "matched", tgt.matched, "fp", tgt.fp[:8])
	s.Audit.Log(auditFindingSuppress,
		"platform", platform, "owner", req.Owner, "repo", req.Repo, "number", req.Number,
		"file", tgt.file, "fingerprint", tgt.fp[:8], "matched", tgt.matched,
		"token_fp", TokenFPFromContext(ctx))
	return &argus.SuppressFindingResponse{Matched: tgt.matched, Fingerprint: tgt.fp}, http.StatusOK
}

// suppressResolve 的包级实现（转发壳见原文件）。
func suppressResolve(s *Service, ctx context.Context, req *argus.SuppressFindingRequest,
	platform string) (suppressTarget, *argus.SuppressFindingResponse, int) {
	str := func(p *string) string {
		if p == nil {
			return ""
		}
		return strings.TrimSpace(*p)
	}
	var tgt suppressTarget
	file := str(req.File)
	comment := str(req.Comment)
	tgt.file, tgt.comment = file, comment
	switch {
	case str(req.Fingerprint) != "":
		// 模式一：报告意见行的 fp 短码直达（前缀解析完整行，含别名回映射）。
		// v3.8.0：取回 prints 行的原始 file/line/severity/comment/plugin——
		// 抑制回写与 pattern 记账都必须用入库身份；请求里的评语（评论路径下是
		// 整条评论原文）只会把学习环账本记到垃圾键上。
		rec, found := s.Store.ResolvePrintRecord(ctx, platform, req.Owner, req.Repo, req.Number, str(req.Fingerprint))
		if !found {
			return tgt, &argus.SuppressFindingResponse{Matched: false, Fingerprint: str(req.Fingerprint)}, http.StatusOK
		}
		tgt.fp = rec.Print
		tgt.pluginName = rec.Plugin
		tgt.file = rec.File
		tgt.resolvedFinding = domain.Finding{
			File: rec.File, Line: rec.Line, Severity: rec.Severity,
			Comment: rec.Comment, By: rec.Plugin,
		}
	case file != "" && comment != "":
		// 模式二：file + 意见原文（v3.7.0）——v2 指纹含行号，原文无法直算；
		// 走近重复检索抑制既有条目（措辞/行号漂移容忍），未命中预插 suppressed 行
		var err2 error
		if tgt.fp, tgt.matched, err2 = s.Store.SuppressPrintNear(ctx, platform, req.Owner, req.Repo, req.Number,
			file, comment, time.Now()); err2 != nil {
			s.log().Error("argus.argus.suppress_near_failed", "error", err2.Error())
			return tgt, nil, http.StatusInternalServerError
		}
		tgt.suppressDone = true
	default:
		// fingerprint 或 (file+comment) 二选一
		return tgt, nil, http.StatusBadRequest
	}
	return tgt, nil, http.StatusOK
}
