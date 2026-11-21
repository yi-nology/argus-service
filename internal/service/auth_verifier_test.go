package service

import (
	"context"
	"errors"
	"testing"
	"time"

	mw "github.com/oekit/middleware"

	"github.com/yi-nology/argus/internal/config"
)

// TokenVerifier 语义：静态 token 有效 / 过期 401 / 未知 401。
func TestTokenVerifierStaticToken(t *testing.T) {
	s := &Service{Cfg: &config.Config{APIToken: "tok-static"}}
	v := tokenVerifier{s: s}

	sub, err := v.Verify(context.Background(), nil, "tok-static")
	if err != nil {
		t.Fatalf("valid token: %v", err)
	}
	if sc, _ := sub.Claims["scope"].(string); sc != "admin" {
		t.Fatalf("scope=%q want admin", sc)
	}

	_, err = v.Verify(context.Background(), nil, "wrong")
	if !errors.Is(err, mw.ErrInvalidToken) {
		t.Fatalf("wrong token err=%v want ErrInvalidToken", err)
	}

	s2 := &Service{Cfg: &config.Config{APITokens: []config.APITokenScope{{
		Token: "tok-exp", Scope: "read", Expires: mustPast(),
	}}}}
	_, err = tokenVerifier{s: s2}.Verify(context.Background(), nil, "tok-exp")
	if !errors.Is(err, mw.ErrInvalidToken) {
		t.Fatalf("expired err=%v want ErrInvalidToken", err)
	}
}

// claimsFrom：Subject.Claims → ctx 五件套。
func TestClaimsFromSubject(t *testing.T) {
	scope, repos, tid, fp, user, role := claimsFrom(mw.Subject{
		Claims: map[string]any{
			"scope": "admin", "repos": "gitea:acme/*", "tenant": "t1",
			"token_fp": "abcd1234", "username": "alice", "role": "maintainer",
		},
	})
	if scope != "admin" || repos != "gitea:acme/*" || tid != "t1" ||
		fp != "abcd1234" || user != "alice" || role != "maintainer" {
		t.Fatalf("claims: %s %s %s %s %s %s", scope, repos, tid, fp, user, role)
	}
	// 空 Claims 安全
	if scope, _, _, _, _, _ := claimsFrom(mw.Subject{}); scope != "" {
		t.Fatal("empty claims should be zero")
	}
}

func mustPast() time.Time { return time.Now().Add(-time.Hour) }
