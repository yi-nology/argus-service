package service

import (
	"context"
	"net/http"
	"testing"

	argus "github.com/yi-nology/argus/biz/model/argus"
	"github.com/yi-nology/argus/internal/storage"
)

func TestAskReviewBadRequest(t *testing.T) {
	s := &Service{}
	_, code := askReview(s, context.Background(), &argus.AskReviewRequest{})
	if code != http.StatusBadRequest {
		t.Fatalf("code=%d want 400", code)
	}
}

func TestRequiredScopeAskRead(t *testing.T) {
	for _, p := range []string{"/ask", "/ask/review"} {
		if got := requiredScope("POST", p); got != "read" {
			t.Errorf("POST %s scope=%q want read", p, got)
		}
		if got := requiredScope("GET", p); got != "read" {
			t.Errorf("GET %s scope=%q want read", p, got)
		}
	}
}

func TestAskReviewNoAuthorize(t *testing.T) {
	// 无 Store/SDK 时 Authorize 对仓库约束 token 返回 false → 403
	// 无约束 token 空 repos → 放行，但 LatestReview 会失败 → ok=false
	s := &Service{}
	// 伪造 scope 让 Authorize 通过：无约束 read token
	ctx := ContextWithScope(context.Background(), "read")
	resp, code := askReview(s, ctx, &argus.AskReviewRequest{
		Platform: "gitea", Owner: "o", Repo: "r", Number: "1",
	})
	// Store 未装配 → 503（fail-closed，不 panic）。
	if code != http.StatusServiceUnavailable {
		t.Fatalf("code=%d want 503 resp=%+v", code, resp)
	}
	_ = storage.PrintOpen
}
