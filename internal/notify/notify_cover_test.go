// notify_cover_test.go —— 终态 IM 推送补测（#36 kernel 60/70）：结论卡/告警
// 文案渲染、HTTP 收发（httptest 桩上游）、状态码与错误记账降级、octopus 客户端
// 头部纪律（X-User-Id 必带 + 可选 Bearer）。
package notify

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/oekit/logx"

	"github.com/yi-nology/argus/internal/notify/octopus"
)

// TestRenderMarkdown 结论卡/告警双形态 + 未知状态不推送。
func TestRenderMarkdown(t *testing.T) {
	done := renderMarkdown(TaskEvent{
		TaskID: "t-1", Owner: "acme", Repo: "app", Number: "42",
		Status: "done", Verdict: "approve", Rule: "R5.1", Findings: 2,
		Tokens: 500, Elapsed: 90 * time.Second,
	})
	for _, want := range []string{"审查完成", "acme/app#42", "approve", "R5.1", "findings: 2", "tokens: 500"} {
		if !strings.Contains(done, want) {
			t.Fatalf("结论卡缺 %q:\n%s", want, done)
		}
	}
	failed := renderMarkdown(TaskEvent{
		TaskID: "t-2", Owner: "acme", Repo: "app", Number: "42",
		Status: "failed", FailReason: strings.Repeat("x", 400),
	})
	if !strings.Contains(failed, "审查失败") || len(failed) > 500 {
		t.Fatalf("告警卡应截断长错误:\n%s", failed)
	}
	if renderMarkdown(TaskEvent{Status: "running"}) != "" {
		t.Fatal("pending/running 不推送")
	}
}

// TestNotifyTerminalHTTP httptest 桩上游：done 推送成功零告警；非 2xx 记账
// （err 计数器只增不断言具体值——指标面为全局 Prometheus，行为面看无 panic）。
func TestNotifyTerminalHTTP(t *testing.T) {
	var gotUser, gotAuth, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUser = r.Header.Get("X-User-Id")
		gotAuth = r.Header.Get("Authorization")
		b := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(b)
		gotBody = string(b)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := &Notifier{BaseURL: srv.URL, ChannelID: 7, User: "argus-bot", Token: "sekret", Log: logx.Nop()}
	n.NotifyTerminal(context.Background(), TaskEvent{
		TaskID: "t-1", Owner: "acme", Repo: "app", Number: "42",
		Status: "done", Verdict: "comment",
	})
	if gotUser != "argus-bot" {
		t.Fatalf("X-User-Id 机器账号纪律: %q", gotUser)
	}
	if gotAuth != "Bearer sekret" {
		t.Fatalf("Bearer 头缺失: %q", gotAuth)
	}
	if !strings.Contains(gotBody, "markdown") || !strings.Contains(gotBody, "审查完成") {
		t.Fatalf("载荷形态失真: %s", gotBody)
	}
}

// TestNotifyDegradePaths 降级面：nil 接收者、未配置 BaseURL、空内容、上游 500
// 与网络错误都只告警不炸。
func TestNotifyDegradePaths(t *testing.T) {
	ctx := context.Background()
	var nilNotifier *Notifier
	nilNotifier.NotifyTerminal(ctx, TaskEvent{Status: "done"}) // nil 接收者 no-op
	nilNotifier.NotifyRaw(ctx, "x")

	n := &Notifier{Log: logx.Nop()} // 未配置 BaseURL
	n.NotifyTerminal(ctx, TaskEvent{Status: "done", TaskID: "t-1"})
	n.NotifyRaw(ctx, "内容")
	n.NotifyRaw(ctx, "   ") // 空内容不推送

	// 上游 500。
	srv500 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv500.Close()
	n500 := &Notifier{BaseURL: srv500.URL, ChannelID: 1, User: "u", Log: logx.Nop()}
	n500.NotifyRaw(ctx, "告警内容")

	// 网络错误（不可达端口）。
	bad := &Notifier{BaseURL: "http://127.0.0.1:1", ChannelID: 1, User: "u", Log: logx.Nop()}
	bad.NotifyRaw(ctx, "告警内容")
	bad.NotifyTerminal(ctx, TaskEvent{Status: "failed", TaskID: "t-9", FailReason: "boom"})
}

// TestOctopusClient octopus 客户端：缺机器账号显式报错、超时缺省 10s。
func TestOctopusClient(t *testing.T) {
	c := octopus.New("http://127.0.0.1:1", 3, "", "", 0, nil)
	if _, err := c.SendMarkdown(context.Background(), "x"); err == nil ||
		!strings.Contains(err.Error(), "机器账号") {
		t.Fatalf("缺 User 应显式报错: %v", err)
	}
	// 超时缺省兜底不炸（零下值 → 10s）。
	c2 := octopus.New("http://127.0.0.1:1", 3, "bot", "", -1, nil)
	if c2 == nil {
		t.Fatal("客户端应构建成功")
	}
}
