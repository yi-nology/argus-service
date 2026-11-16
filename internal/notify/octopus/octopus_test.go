package octopus

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestNewTimeoutAndClientInjection New 的构造契约：timeout<=0 取 10s 缺省、
// hc=nil 时内置客户端；注入的 hc 原样使用（不覆盖其 Timeout）；字段透传。
func TestNewTimeoutAndClientInjection(t *testing.T) {
	for _, timeout := range []time.Duration{0, -time.Second} {
		c := New("http://octopus.svc", 1, "bot", "", timeout, nil)
		if c.hc == nil {
			t.Fatalf("timeout=%v 时 New 必须内置 http.Client", timeout)
		}
		if got := c.hc.Timeout; got != 10*time.Second {
			t.Fatalf("timeout=%v 时缺省 timeout = %v, want 10s", timeout, got)
		}
	}

	c := New("http://octopus.svc", 1, "bot", "", 3*time.Second, nil)
	if c.hc.Timeout != 3*time.Second {
		t.Fatalf("正 timeout 未生效: %v", c.hc.Timeout)
	}

	custom := &http.Client{Timeout: time.Minute}
	c = New("http://octopus.svc", 7, "svc-user", "tk", 3*time.Second, custom)
	if c.hc != custom {
		t.Fatal("注入的 hc 必须原样使用")
	}
	if custom.Timeout != time.Minute {
		t.Fatalf("注入 hc 的 Timeout 被改写: %v", custom.Timeout)
	}
	if c.BaseURL != "http://octopus.svc" || c.ChannelID != 7 || c.User != "svc-user" || c.Token != "tk" {
		t.Fatalf("字段透传不符: %+v", c)
	}
}

// capture 记录服务端收到的请求（handler 与测试跨 goroutine，读写加锁）。
type capture struct {
	mu                                             sync.Mutex
	hits                                           int
	method, path, contentType, user, auth, rawBody string
}

func (c *capture) snapshot() capture {
	c.mu.Lock()
	defer c.mu.Unlock()
	return capture{
		hits: c.hits, method: c.method, path: c.path,
		contentType: c.contentType, user: c.user, auth: c.auth, rawBody: c.rawBody,
	}
}

func captureServer(t *testing.T, status int, responseBody string) (*httptest.Server, *capture) {
	t.Helper()
	cap := &capture{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		cap.mu.Lock()
		cap.hits++
		cap.method = r.Method
		cap.path = r.URL.Path
		cap.contentType = r.Header.Get("Content-Type")
		cap.user = r.Header.Get("X-User-Id")
		cap.auth = r.Header.Get("Authorization")
		cap.rawBody = string(body)
		cap.mu.Unlock()
		w.WriteHeader(status)
		_, _ = io.WriteString(w, responseBody)
	}))
	t.Cleanup(ts.Close)
	return ts, cap
}

// TestSendMarkdownRequest 成功路径：POST /api/v2/send/quick，去尾斜杠、
// JSON 头、机器账号头、Bearer、请求体 msgType/content/channelId 全部可断言。
func TestSendMarkdownRequest(t *testing.T) {
	ts, cap := captureServer(t, http.StatusOK, `{"code":0}`)
	c := New(ts.URL+"/", 42, "argus-bot", "tok-1", 0, nil)

	code, err := c.SendMarkdown(context.Background(), "# 标题\n正文")
	if err != nil {
		t.Fatalf("SendMarkdown error: %v", err)
	}
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}

	got := cap.snapshot()
	if got.hits != 1 {
		t.Fatalf("请求次数 = %d, want 1", got.hits)
	}
	if got.method != http.MethodPost {
		t.Fatalf("method = %q", got.method)
	}
	if got.path != "/api/v2/send/quick" {
		t.Fatalf("path = %q（尾斜杠未去重？）", got.path)
	}
	if got.contentType != "application/json" {
		t.Fatalf("Content-Type = %q", got.contentType)
	}
	if got.user != "argus-bot" {
		t.Fatalf("X-User-Id = %q", got.user)
	}
	if got.auth != "Bearer tok-1" {
		t.Fatalf("Authorization = %q", got.auth)
	}
	var payload struct {
		ChannelID int64  `json:"channelId"`
		Content   string `json:"content"`
		MsgType   string `json:"msgType"`
	}
	if err := json.Unmarshal([]byte(got.rawBody), &payload); err != nil {
		t.Fatalf("请求体不是 JSON: %v (%s)", err, got.rawBody)
	}
	if payload.ChannelID != 42 || payload.Content != "# 标题\n正文" || payload.MsgType != "markdown" {
		t.Fatalf("请求体不符: %+v", payload)
	}
}

// TestSendMarkdownNoToken 未配置 Token 时不带 Authorization 头。
func TestSendMarkdownNoToken(t *testing.T) {
	ts, cap := captureServer(t, http.StatusOK, "")
	c := New(ts.URL, 1, "bot", "", 0, nil)
	if code, err := c.SendMarkdown(context.Background(), "x"); err != nil || code != 200 {
		t.Fatalf("code=%d err=%v", code, err)
	}
	if got := cap.snapshot(); got.auth != "" {
		t.Fatalf("空 Token 不应带 Authorization: %q", got.auth)
	}
}

// TestSendMarkdownStatuses 非 2xx 不吞成 error：状态码原样返回，由调用方记账。
func TestSendMarkdownStatuses(t *testing.T) {
	for _, status := range []int{201, 204, 400, 404, 500} {
		ts, _ := captureServer(t, status, "ignored")
		c := New(ts.URL, 1, "bot", "", 0, nil)
		code, err := c.SendMarkdown(context.Background(), "x")
		if err != nil {
			t.Fatalf("status %d: 不应返回 error: %v", status, err)
		}
		if code != status {
			t.Fatalf("status 返回 %d, want %d", code, status)
		}
	}
}

// TestSendMarkdownNonJSONResponse 响应体不是 JSON 也只看状态码（不解析响应）。
func TestSendMarkdownNonJSONResponse(t *testing.T) {
	ts, _ := captureServer(t, http.StatusOK, "not-json at all")
	c := New(ts.URL, 1, "bot", "", 0, nil)
	code, err := c.SendMarkdown(context.Background(), "x")
	if err != nil || code != http.StatusOK {
		t.Fatalf("非 JSON 响应: code=%d err=%v", code, err)
	}
}

// TestSendMarkdownMissingUser 缺机器账号必须在发送前报错（匿名推送不可归因）。
func TestSendMarkdownMissingUser(t *testing.T) {
	ts, cap := captureServer(t, http.StatusOK, "")
	c := New(ts.URL, 1, "", "", 0, nil)
	code, err := c.SendMarkdown(context.Background(), "x")
	if err == nil {
		t.Fatal("缺 User 必须报错")
	}
	if code != 0 {
		t.Fatalf("未发送时 status 应为 0, got %d", code)
	}
	if !strings.Contains(err.Error(), "User") {
		t.Fatalf("错误信息应可归因到缺 User: %v", err)
	}
	if hits := cap.snapshot().hits; hits != 0 {
		t.Fatalf("缺 User 不得发送请求, hits=%d", hits)
	}
}

// TestSendMarkdownNetworkError 端点不可达：返回 (0, err)，不 panic。
func TestSendMarkdownNetworkError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := ts.URL
	ts.Close()
	c := New(url, 1, "bot", "", 0, nil)
	code, err := c.SendMarkdown(context.Background(), "x")
	if err == nil {
		t.Fatal("网络错误必须返回 error")
	}
	if code != 0 {
		t.Fatalf("网络错误 status 应为 0, got %d", code)
	}
}

// TestSendMarkdownInvalidBaseURL 非法 BaseURL 在构造请求时归因报错（不 panic）。
func TestSendMarkdownInvalidBaseURL(t *testing.T) {
	c := New("://missing-scheme", 1, "bot", "", 0, nil)
	code, err := c.SendMarkdown(context.Background(), "x")
	if err == nil || !strings.Contains(err.Error(), "missing protocol scheme") {
		t.Fatalf("非法 URL 错误应可归因: code=%d err=%v", code, err)
	}
	if code != 0 {
		t.Fatalf("status = %d, want 0", code)
	}
}

// TestSendMarkdownContextCanceled 调用方取消应保留 context.Canceled 归因。
func TestSendMarkdownContextCanceled(t *testing.T) {
	ts, cap := captureServer(t, http.StatusOK, "")
	c := New(ts.URL, 1, "bot", "", 0, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	code, err := c.SendMarkdown(ctx, "x")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("应归因 context.Canceled: code=%d err=%v", code, err)
	}
	if code != 0 {
		t.Fatalf("status = %d, want 0", code)
	}
	if hits := cap.snapshot().hits; hits != 0 {
		t.Fatalf("取消的请求不应到达服务端, hits=%d", hits)
	}
}
