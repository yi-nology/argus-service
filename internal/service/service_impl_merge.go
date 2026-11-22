// service_impl_merge.go —— 合并资格门判定（fail-closed 七条）与 IDL 映射。
// HTTP 面（eligibility/preview/confirm/get/reconcile）见 service_impl_merge_api.go。
//
// 资格门（缺一不放行）：
//  1. cr_open / 2. not_draft / 3. head_match / 4. mergeable
//  5. review_done / 6. no_blocking_findings / 7. ci_pass（unknown = 阻断）
package service

import (
	"context"
	"strings"

	"github.com/yi-nology/go-git-platform/model"

	argus "github.com/yi-nology/argus/biz/model/argus"
)

// 合资格门稳定机器码（前端/审计按码分支，不解析 message）。
const (
	gateCROpen     = "cr_open"
	gateNotDraft   = "not_draft"
	gateHeadMatch  = "head_match"
	gateMergeable  = "mergeable"
	gateReviewDone = "review_done"
	gateNoBlocking = "no_blocking_findings"
	gateCIPass     = "ci_pass"
)

// mergeGateResult 资格门内部投影（序列化进 GateJSON / 映射到 IDL）。
type mergeGateResult struct {
	Eligible         bool             `json:"eligible"`
	HeadSHA          string           `json:"head_sha"`
	MergeStatus      string           `json:"merge_status"`
	ReviewTaskID     string           `json:"review_task_id,omitempty"`
	ReviewVerdict    string           `json:"review_verdict,omitempty"`
	CIStatus         string           `json:"ci_status"`
	BlockingFindings int64            `json:"blocking_findings"`
	Checks           []mergeGateCheck `json:"checks"`
}

type mergeGateCheck struct {
	Code    string `json:"code"`
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}

func (g *mergeGateResult) firstFailCode() string {
	for _, c := range g.Checks {
		if !c.OK {
			return c.Code
		}
	}
	return ""
}

// mergeAdmin 二次判定：合并是最高危远端写——**业务裁定面**（maintainer 或
// 部署超管），平台管理角色 admin 刻意不可（防越权合码，MergePilot 同款）。
func mergeAdmin(ctx context.Context) bool { return canDecide(ctx) }

// mergeDeny 403 留痕。
func mergeDeny(s *Service, ctx context.Context, action, subject string) {
	authzDeniedMetric(ctx, "merge."+action)
	s.log().Warn("argus.merge.denied", "action", action, "subject", subject,
		"token_fp", TokenFPFromContext(ctx))
}

// evaluateMergeGate 统一资格门（只读远端 + 本地库；不写任何远端状态）。
// expectedHead 非空时额外要求远端 head 与之相等（confirm 绑定用）。
func evaluateMergeGate(s *Service, ctx context.Context, platform, owner, repo, number, expectedHead string) (*mergeGateResult, error) {
	g := &mergeGateResult{CIStatus: "unknown"}
	add := func(code string, ok bool, msg string) {
		g.Checks = append(g.Checks, mergeGateCheck{Code: code, OK: ok, Message: msg})
	}

	if s.SDK == nil {
		add(gateCROpen, false, "sdk 未装配")
		g.Eligible = false
		return g, nil
	}
	cr, err := s.SDK.GetCR(ctx, platform, owner, repo, number)
	if err != nil {
		return nil, err
	}
	if cr == nil {
		add(gateCROpen, false, "平台未返回 CR")
		g.Eligible = false
		return g, nil
	}

	// 1. cr_open
	open := cr.State == model.CRStateOpened
	add(gateCROpen, open, "state="+string(cr.State))

	// 2. not_draft
	add(gateNotDraft, !cr.Draft, map[bool]string{true: "not draft", false: "draft/WIP"}[cr.Draft])

	// 3. head_match
	head := cr.HeadSHA
	g.HeadSHA = head
	if head == "" {
		add(gateHeadMatch, false, "远端 head 为空（未知不放行）")
	} else if expectedHead != "" && !strings.EqualFold(head, expectedHead) {
		add(gateHeadMatch, false, "远端 head 与 expected 不一致（陈旧绑定）")
	} else {
		add(gateHeadMatch, true, "head="+head)
	}

	// 4. mergeable（平台词表：mergeable / can_be_merged = 可合并）
	g.MergeStatus = cr.MergeStatus
	ms := strings.ToLower(strings.TrimSpace(cr.MergeStatus))
	mergeable := ms == "mergeable" || ms == "can_be_merged"
	add(gateMergeable, mergeable, "merge_status="+or(cr.MergeStatus, "(空=未知)"))

	// 5. review_done + 6. no_blocking（当前 head）
	if g.HeadSHA != "" {
		task, terr := s.Store.LatestReviewTaskForHead(ctx, platform, owner, repo, number, g.HeadSHA)
		if terr != nil {
			return nil, terr
		}
		if task != nil {
			g.ReviewTaskID = task.ID
			g.ReviewVerdict = task.Verdict
			add(gateReviewDone, true, "task="+task.ID+" verdict="+task.Verdict)
			ids, ierr := s.Store.ReviewTaskIDsForHead(ctx, platform, owner, repo, number, g.HeadSHA)
			if ierr != nil {
				return nil, ierr
			}
			n, ferr := s.Store.CountBlockingFindings(ctx, platform, owner, repo, number, ids)
			if ferr != nil {
				return nil, ferr
			}
			g.BlockingFindings = n
			add(gateNoBlocking, n == 0, "blocking="+itoa64(n))
		} else {
			add(gateReviewDone, false, "当前 head 无已完成审查")
			add(gateNoBlocking, false, "无当前 head 审查任务，无法确认发现清零")
			g.BlockingFindings = -1
		}
	} else {
		add(gateReviewDone, false, "head 未知")
		add(gateNoBlocking, false, "head 未知")
	}

	// 7. ci_pass（fail-closed：unknown 一律阻断）
	ci, detail := s.SDK.EvalCIStatus(ctx, platform, owner, repo, g.HeadSHA)
	g.CIStatus = ci
	add(gateCIPass, ci == CIStateSuccessPass, ci+": "+detail)

	g.Eligible = true
	for _, c := range g.Checks {
		if !c.OK {
			g.Eligible = false
			break
		}
	}
	return g, nil
}

// CIStateSuccessPass argusctx.CIStateSuccess 的别名（避免 service↔argusctx
// 常量重复漂移；值必须与 argusctx.CIStateSuccess 一致，由测试钉住）。
const CIStateSuccessPass = "success"

func or(a, b string) string {
	if a == "" {
		return b
	}
	return a
}

func itoa64(n int64) string {
	if n < 0 {
		return "?"
	}
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

// toIDLGate 映射内部投影 → IDL。
func toIDLGate(g *mergeGateResult) *argus.MergeEligibility {
	out := &argus.MergeEligibility{
		Eligible:         g.Eligible,
		HeadSha:          g.HeadSHA,
		MergeStatus:      g.MergeStatus,
		ReviewTaskID:     g.ReviewTaskID,
		ReviewVerdict:    g.ReviewVerdict,
		CiStatus:         g.CIStatus,
		BlockingFindings: int32(g.BlockingFindings),
	}
	for _, c := range g.Checks {
		out.Checks = append(out.Checks, &argus.MergeGateCheck{
			Code: c.Code, Ok: c.OK, Message: c.Message,
		})
	}
	return out
}
