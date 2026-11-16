// octopus_cover_test.go —— octopus 客户端补测（#36 kernel 60/70）：POST
// /api/v2/send/quick 收发、X-User-Id 机器账号头纪律、Bearer 可选头、缺账号
// 显式报错（匿名推送不可归因红线）、超时缺省兜底。
package octopus

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSendMarkdownWire(t *testing.T) {
	var gotUser, gotAuth, gotPath, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotUser = r.Header.Get("X-User-Id")
		gotAuth = r.Header.Get("Authorization")
		b := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(b)
		gotBody = string(b)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := New(srv.URL, 42, "argus-bot", "tk", 5*time.Second, nil)
	code, err := c.SendMarkdown(context.Background(), "**hello**")
	if err != nil || code != http.StatusOK {
		t.Fatalf("推送应成功: code=%d err=%v", code, err)
	}
	if gotPath != "/api/v2/send/quick" {
		t.Fatalf("端点路径失真: %s", gotPath)
	}
	if gotUser != "argus-bot" {
		t.Fatalf("X-User-Id 必带: %q", gotUser)
	}
	if gotAuth != "Bearer tk" {
		t.Fatalf("Bearer 头失真: %q", gotAuth)
	}
	if !strings.Contains(gotBody, `"msgType":"markdown"`) || !strings.Contains(gotBody, `"channelId":42`) {
		t.Fatalf("载荷形态失真: %s", gotBody)
	}

	// 无 Token：无 Authorization 头（网关可选语义）。
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			w.WriteHeader(http.StatusTeapot)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv2.Close()
	code2, err := New(srv2.URL, 1, "bot", "", 0, nil).SendMarkdown(context.Background(), "x")
	if err != nil || code2 != http.StatusOK {
		t.Fatalf("无 Token 应省略头: code=%d err=%v", code2, err)
	}
}

func TestSendMarkdownNoUserFails(t *testing.T) {
	c := New("http://127.0.0.1:1", 1, "", "", 0, nil)
	if _, err := c.SendMarkdown(context.Background(), "x"); err == nil ||
		!strings.Contains(err.Error(), "机器账号") {
		t.Fatalf("缺账号应显式报错（匿名不可归因）: %v", err)
	}
}

func TestSendMarkdownNetworkError(t *testing.T) {
	c := New("http://127.0.0.1:1", 1, "bot", "", 500*time.Millisecond, nil)
	if code, err := c.SendMarkdown(context.Background(), "x"); err == nil || code != 0 {
		t.Fatalf("网络错误 code=0: code=%d err=%v", code, err)
	}
}
