// service_ask.go —— 审查问答 HTTP 面（与 MCP ask_argus 同源 ReviewQA）。
package service

import (
	"context"
	"net/http"

	argus "github.com/yi-nology/argus/biz/model/argus"
)

// AskReview POST /ask：question 空=最新结论；非空=基于报告追问（read 档）。
func (s *Service) AskReview(ctx context.Context, req *argus.AskReviewRequest) (*argus.AskReviewResponse, int) {
	return askReview(s, ctx, req)
}

// AskLatest GET /ask/review：只取最新审查结论（read 档）。
func (s *Service) AskLatest(ctx context.Context, req *argus.AskLatestRequest) (*argus.AskReviewResponse, int) {
	return askReview(s, ctx, &argus.AskReviewRequest{
		Platform: req.GetPlatform(), Owner: req.GetOwner(),
		Repo: req.GetRepo(), Number: req.GetNumber(),
	})
}

func askReview(s *Service, ctx context.Context, req *argus.AskReviewRequest) (*argus.AskReviewResponse, int) {
	platform, owner, repo, number := req.GetPlatform(), req.GetOwner(), req.GetRepo(), req.GetNumber()
	if platform == "" || owner == "" || repo == "" || number == "" {
		return nil, http.StatusBadRequest
	}
	if !s.Authorize(ctx, platform, owner, repo) {
		return nil, http.StatusForbidden
	}

	question := req.GetQuestion()
	if s.Store == nil {
		return &argus.AskReviewResponse{Ok: false, Message: "存储未装配"}, http.StatusServiceUnavailable
	}
	if question != "" {
		ans, ok := s.AnswerAboutReview(ctx, platform, owner, repo, number, question)
		if !ok {
			return &argus.AskReviewResponse{
				Ok:      false,
				Message: "无法回答：未配置 LLM、无审查报告或引擎不可用",
			}, http.StatusOK
		}
		return &argus.AskReviewResponse{Ok: true, Answer: strPtr(ans), Message: "ok"}, http.StatusOK
	}

	rev, ok := s.LatestReview(ctx, platform, owner, repo, number)
	if !ok {
		return &argus.AskReviewResponse{
			Ok:      false,
			Message: "尚无审查结论（先建审查任务）",
		}, http.StatusOK
	}
	out := map[string]string{}
	for k, v := range rev {
		if sv, ok2 := v.(string); ok2 {
			out[k] = sv
		}
	}
	return &argus.AskReviewResponse{
		Ok:      true,
		Review:  out,
		Message: "ok",
	}, http.StatusOK
}
