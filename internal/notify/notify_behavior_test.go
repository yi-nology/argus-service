package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/oekit/logx"
)

// recLog 记录 Warn 调用（含 kv），用于断言失败降级的可归因日志。
type recLog struct {
	nopLog
	mu      sync.Mutex
	entries []string
}

func (r *recLog) Warn(msg string, kv ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries = append(r.entries, fmt.Sprintf("%s %v", msg, kv))
}

func (r *recLog) warns() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.entries...)
}

var _ logx.Logger = (*recLog)(nil)

type sentRequest struct {
	method, path, contentType, user, auth, body string
}

type reqCapture struct {
	mu   sync.Mutex
	reqs []sentRequest
}

func (c *reqCapture) snapshot() []sentRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]sentRequest(nil), c.reqs...)
}

func newNotifyServer(t *testing.T, status int) (*httptest.Server, *reqCapture) {
	t.Helper()
	cap := &reqCapture{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		cap.mu.Lock()
		cap.reqs = append(cap.reqs, sentRequest{
			method: r.Method, path: r.URL.Path,
			contentType: r.Header.Get("Content-Type"),
			user:        r.Header.Get("X-User-Id"),
			auth:        r.Header.Get("Authorization"),
			body:        string(body),
		})
		cap.mu.Unlock()
		w.WriteHeader(status)
		_, _ = io.WriteString(w, `{"code":0}`)
	}))
	t.Cleanup(ts.Close)
	return ts, cap
}

func decodePayload(t *testing.T, raw string) (channelID int64, msgType, content string) {
	t.Helper()
	var p struct {
		ChannelID int64  `json:"channelId"`
		MsgType   string `json:"msgType"`
		Content   string `json:"content"`
	}
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		t.Fatalf("请求体不是 JSON: %v (%s)", err, raw)
	}
	return p.ChannelID, p.MsgType, p.Content
}

// TestRenderMarkdown 终态文案：done/stale 结论卡（verdict/rule/findings/
// tokens/耗时）、failed 告警（FailReason 300 rune 截断）、非终态空串。
func TestRenderMarkdown(t *testing.T) {
	base := TaskEvent{
		TaskID: "t1", Owner: "acme", Repo: "app", Number: "5",
		Verdict: "request_changes", Rule: "R5.1", Findings: 2, Tokens: 1234,
		Elapsed: 90 * time.Second,
	}
	with := func(mut func(*TaskEvent)) TaskEvent {
		e := base
		mut(&e)
		return e
	}
	reason300 := strings.Repeat("错", 300)

	cases := []struct {
		name string
		ev   TaskEvent
		want string
	}{
		{
			name: "done",
			ev:   with(func(e *TaskEvent) { e.Status = "done" }),
			want: "**Argus 审查完成** acme/app#5\n- 结论: `request_changes`（R5.1）\n" +
				"- findings: 2\n- 用时: 1m30s / tokens: 1234\n- task: t1",
		},
		{
			name: "stale",
			ev: with(func(e *TaskEvent) {
				e.Status = "stale"
				e.Verdict = "approve"
				e.Rule = "R1"
			}),
			want: "**Argus 审查完成** acme/app#5\n- 结论: `approve`（R1）\n" +
				"- findings: 2\n- 用时: 1m30s / tokens: 1234\n- task: t1",
		},
		{
			name: "failed_short_reason",
			ev: with(func(e *TaskEvent) {
				e.Status = "failed"
				e.TaskID = "t9"
				e.FailReason = "boom"
			}),
			want: "**Argus 审查失败** acme/app#5\n- 原因: boom\n- task: t9",
		},
		{
			name: "failed_reason_at_limit",
			ev: with(func(e *TaskEvent) {
				e.Status = "failed"
				e.TaskID = "t9"
				e.FailReason = reason300
			}),
			want: "**Argus 审查失败** acme/app#5\n- 原因: " + reason300 + "\n- task: t9",
		},
		{
			name: "failed_reason_truncated",
			ev: with(func(e *TaskEvent) {
				e.Status = "failed"
				e.TaskID = "t9"
				e.FailReason = reason300 + "错"
			}),
			want: "**Argus 审查失败** acme/app#5\n- 原因: " + reason300 + "…\n- task: t9",
		},
		{
			name: "elapsed_rounds_half_up",
			ev: with(func(e *TaskEvent) {
				e.Status = "done"
				e.Elapsed = 1500 * time.Millisecond
			}),
			want: "**Argus 审查完成** acme/app#5\n- 结论: `request_changes`（R5.1）\n" +
				"- findings: 2\n- 用时: 2s / tokens: 1234\n- task: t1",
		},
		{name: "pending", ev: with(func(e *TaskEvent) { e.Status = "pending" }), want: ""},
		{name: "running", ev: with(func(e *TaskEvent) { e.Status = "running" }), want: ""},
		{name: "empty_status", ev: with(func(e *TaskEvent) { e.Status = "" }), want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := renderMarkdown(tc.ev); got != tc.want {
				t.Fatalf("renderMarkdown:\n got %q\nwant %q", got, tc.want)
			}
		})
	}
}

func TestMachineUserDefault(t *testing.T) {
	n := &Notifier{}
	if got := n.machineUser(); got != "argus-bot" {
		t.Fatalf("缺省机器账号 = %q", got)
	}
	n.User = "svc-account"
	if got := n.machineUser(); got != "svc-account" {
		t.Fatalf("显式机器账号被覆盖: %q", got)
	}
}

// TestNotifyTerminalRequest 端到端请求：缺省机器账号、Bearer、channel、
// markdown 类型，content 与 renderMarkdown 完全一致；成功不发 warn。
func TestNotifyTerminalRequest(t *testing.T) {
	ts, cap := newNotifyServer(t, http.StatusOK)
	log := &recLog{}
	n := &Notifier{BaseURL: ts.URL, ChannelID: 9, Token: "tk", Log: log}
	ev := TaskEvent{
		TaskID: "t42", Owner: "o", Repo: "r", Number: "7",
		Status: "done", Verdict: "approve", Rule: "R2",
		Findings: 1, Tokens: 10, Elapsed: 2 * time.Second,
	}
	n.NotifyTerminal(context.Background(), ev)

	reqs := cap.snapshot()
	if len(reqs) != 1 {
		t.Fatalf("请求次数 = %d, want 1", len(reqs))
	}
	got := reqs[0]
	if got.method != http.MethodPost || got.path != "/api/v2/send/quick" {
		t.Fatalf("请求行 = %s %s", got.method, got.path)
	}
	if got.contentType != "application/json" {
		t.Fatalf("Content-Type = %q", got.contentType)
	}
	if got.user != "argus-bot" {
		t.Fatalf("缺省 X-User-Id = %q", got.user)
	}
	if got.auth != "Bearer tk" {
		t.Fatalf("Authorization = %q", got.auth)
	}
	channelID, msgType, content := decodePayload(t, got.body)
	if channelID != 9 || msgType != "markdown" {
		t.Fatalf("payload channel/type = %d/%q", channelID, msgType)
	}
	if want := renderMarkdown(ev); content != want {
		t.Fatalf("推送 content 与渲染不一致:\n got %q\nwant %q", content, want)
	}
	if warns := log.warns(); len(warns) != 0 {
		t.Fatalf("成功推送不应 warn: %v", warns)
	}
}

// TestNotifyTerminalSkipsNonTerminal pending/running 不推送（零请求）。
func TestNotifyTerminalSkipsNonTerminal(t *testing.T) {
	ts, cap := newNotifyServer(t, http.StatusOK)
	n := &Notifier{BaseURL: ts.URL, Log: nopLog{}}
	for _, status := range []string{"pending", "running", ""} {
		n.NotifyTerminal(context.Background(), TaskEvent{Status: status, Owner: "o", Repo: "r", Number: "1"})
	}
	if reqs := cap.snapshot(); len(reqs) != 0 {
		t.Fatalf("非终态不得推送, got %d 请求", len(reqs))
	}
}

// TestNotifyDisabled 禁用面（nil 接收者/空 URL/空 Log/空白 Raw 内容）零请求不 panic。
func TestNotifyDisabled(t *testing.T) {
	ts, cap := newNotifyServer(t, http.StatusOK)

	var nilN *Notifier
	nilN.NotifyTerminal(context.Background(), TaskEvent{Status: "done"})
	nilN.NotifyRaw(context.Background(), "x")

	(&Notifier{Log: nopLog{}}).NotifyTerminal(context.Background(), TaskEvent{Status: "done"})
	(&Notifier{BaseURL: ts.URL}).NotifyTerminal(context.Background(), TaskEvent{Status: "done"}) // Log nil
	(&Notifier{BaseURL: ts.URL, Log: nopLog{}}).NotifyRaw(context.Background(), " \n\t ")
	(&Notifier{BaseURL: ts.URL, Log: nopLog{}}).NotifyRaw(context.Background(), "")

	if reqs := cap.snapshot(); len(reqs) != 0 {
		t.Fatalf("禁用面不得发起请求, got %d", len(reqs))
	}
}

// TestNotifyRaw 通用 markdown 推送使用同一客户端/缺省账号，空内容不推送。
func TestNotifyRaw(t *testing.T) {
	ts, cap := newNotifyServer(t, http.StatusOK)
	n := &Notifier{BaseURL: ts.URL, ChannelID: 3, Log: nopLog{}}
	n.NotifyRaw(context.Background(), "# 健康报告\nall green")

	reqs := cap.snapshot()
	if len(reqs) != 1 {
		t.Fatalf("请求次数 = %d, want 1", len(reqs))
	}
	if reqs[0].user != "argus-bot" || reqs[0].auth != "" {
		t.Fatalf("Raw 头不符: user=%q auth=%q", reqs[0].user, reqs[0].auth)
	}
	channelID, msgType, content := decodePayload(t, reqs[0].body)
	if channelID != 3 || msgType != "markdown" || content != "# 健康报告\nall green" {
		t.Fatalf("Raw payload 不符: channel=%d type=%q content=%q", channelID, msgType, content)
	}

	n.NotifyRaw(context.Background(), "   ")
	if got := len(cap.snapshot()); got != 1 {
		t.Fatalf("空白内容不应追加请求, got %d", got)
	}
}

// TestNotifyFailureDegradation 失败不阻断主链路：非 2xx 与网络错误各记一条
// 可归因 warn（含状态码/任务号），调用不 panic。
func TestNotifyFailureDegradation(t *testing.T) {
	t.Run("terminal_rejected", func(t *testing.T) {
		ts, _ := newNotifyServer(t, http.StatusInternalServerError)
		log := &recLog{}
		n := &Notifier{BaseURL: ts.URL, Log: log}
		n.NotifyTerminal(context.Background(), TaskEvent{
			TaskID: "t1", Owner: "o", Repo: "r", Number: "1", Status: "done",
		})
		warns := log.warns()
		if len(warns) != 1 {
			t.Fatalf("warn 条数 = %d, want 1", len(warns))
		}
		for _, want := range []string{"argus.notify.send_rejected", "task", "t1", "500"} {
			if !strings.Contains(warns[0], want) {
				t.Fatalf("warn 缺少 %q: %s", want, warns[0])
			}
		}
	})

	t.Run("raw_rejected_uses_detail_branch", func(t *testing.T) {
		ts, _ := newNotifyServer(t, http.StatusServiceUnavailable)
		log := &recLog{}
		n := &Notifier{BaseURL: ts.URL, Log: log}
		n.NotifyRaw(context.Background(), "x")
		warns := log.warns()
		if len(warns) != 1 {
			t.Fatalf("warn 条数 = %d, want 1", len(warns))
		}
		for _, want := range []string{"argus.notify.raw_rejected", "detail", "503"} {
			if !strings.Contains(warns[0], want) {
				t.Fatalf("warn 缺少 %q: %s", want, warns[0])
			}
		}
	})

	t.Run("network_error", func(t *testing.T) {
		ts, _ := newNotifyServer(t, http.StatusOK)
		url := ts.URL
		ts.Close()
		log := &recLog{}
		n := &Notifier{BaseURL: url, Log: log}
		n.NotifyTerminal(context.Background(), TaskEvent{
			TaskID: "t2", Owner: "o", Repo: "r", Number: "2", Status: "failed", FailReason: "x",
		})
		warns := log.warns()
		if len(warns) != 1 || !strings.Contains(warns[0], "argus.notify.send_failed") ||
			!strings.Contains(warns[0], "t2") {
			t.Fatalf("网络错误 warn 不符: %v", warns)
		}
	})
}
