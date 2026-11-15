// service_taskapi_suppress.go —— 误报抑制写面（自 service_taskapi.go 拆出）。

package service

import (
	"context"
	"net/http"
	"strings"
	"time"

	argus "github.com/yi-nology/argus/biz/model/argus"
	"github.com/yi-nology/argus/internal/domain"
	"github.com/yi-nology/argus/internal/storage"
)

// SuppressFinding 误报反馈（🔒，设计 §3.3 反馈通道最小入口）：
// 服务端按 Merger 同一指纹函数计算（file + 规范化评论文本哈希，行号不入指纹），
// 置 suppressed 后每轮报告自动剔除该意见。下一轮审查起生效（总结评论不即时重渲）。
func (s *Service) SuppressFinding(ctx context.Context, req *argus.SuppressFindingRequest) (*argus.SuppressFindingResponse, int) {
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

// suppressTarget SuppressFinding 两形态解析出的目标身份与处置状态。
type suppressTarget struct {
	fp              string
	file            string
	comment         string
	pluginName      string
	matched         bool
	suppressDone    bool
	resolvedFinding domain.Finding // 模式一解析出的入库原始身份（模式二为零值，仍用请求身份）
}

// suppressResolve 抑制目标解析：模式一（fp 短码直达）/ 模式二（file+原文近重复）
// 二选一，解析期即定的终态以 resp 非 nil 透出（未匹配 200 / 形态非法 400 /
// 近重复检索失败 500）；否则返回目标身份交调用方继续抑制回写。
func (s *Service) suppressResolve(ctx context.Context, req *argus.SuppressFindingRequest,
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
