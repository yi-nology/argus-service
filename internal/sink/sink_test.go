package sink

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/oekit/logx"

	"github.com/yi-nology/argus/internal/domain"
	"github.com/yi-nology/argus/internal/plugin"
)

// recordLogger 捕获上报告警（内核 logx 接口嵌入继承其余方法）。
type recordLogger struct {
	logx.Logger
	mu    sync.Mutex
	warns []string
}

func (l *recordLogger) Warn(msg string, _ ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.warns = append(l.warns, msg)
}

func (l *recordLogger) warnCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.warns)
}

func sampleFindings() []domain.Finding {
	return []domain.Finding{
		{FP: "fp-1", File: "a.go", Line: 3, Severity: "high", Comment: "竞态", SuggestedFix: "加锁", By: "ocr"},
		{FP: "fp-2", File: "b.go", Line: 9, Severity: "low", Comment: "命名"},
	}
}

// 契约映射：字段逐一对齐 review-service ingest 契约（argus_task_id/业务键/
// verdict/findings 含 fp/severity/plugins/统计）。
func TestBuildPayloadContract(t *testing.T) {
	pr := &plugin.PRContext{
		Platform: "gitea", Owner: "demo-user", Repo: "demo", Number: "7",
		Title: "fix login", HeadSHA: "abc123", BaseBranch: "main",
	}
	now := time.Now()
	p := BuildPayload("task-1", pr, sampleFindings(), "comment",
		[]string{"correctness", "ocr"}, 1500, 42, now)
	if p.ArgusTaskID != "task-1" || p.Platform != "gitea" || p.Owner != "demo-user" ||
		p.Repo != "demo" || p.Number != "7" || p.HeadSHA != "abc123" || p.BaseBranch != "main" {
		t.Fatalf("业务键映射不符: %+v", p)
	}
	if p.Verdict != "comment" || len(p.Plugins) != 2 || p.DurationMS != 1500 || p.TokenUsage != 42 {
		t.Fatalf("统计映射不符: %+v", p)
	}
	if len(p.Findings) != 2 || p.Findings[0].FP != "fp-1" || p.Findings[0].Severity != "high" ||
		p.Findings[0].SuggestedFix != "加锁" {
		t.Fatalf("findings 映射不符: %+v", p.Findings)
	}
	if _, err := json.Marshal(p); err != nil {
		t.Fatalf("payload 须可序列化: %v", err)
	}
}

// 意见超上限截断（保序）。
func TestBuildPayloadTruncates(t *testing.T) {
	fs := make([]domain.Finding, MaxFindings+10)
	for i := range fs {
		fs[i] = domain.Finding{FP: "x", File: "f.go", Severity: "low", Comment: "c"}
	}
	p := BuildPayload("t", &plugin.PRContext{}, fs, "comment", nil, 0, 0, time.Now())
	if len(p.Findings) != MaxFindings {
		t.Fatalf("应截断到 %d，got %d", MaxFindings, len(p.Findings))
	}
}

// 上报端到端：POST 契约 JSON + Bearer 头；对端 500 时重试后放弃且不 panic。
func TestReportPostsAndRetries(t *testing.T) {
	var calls atomic.Int32
	var gotAuth, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		gotAuth = r.Header.Get("Authorization")
		buf := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(buf)
		gotBody = string(buf)
		// 对端契约：成功恒回 JSON 对象（review-service IngestHandler 形态）
		_, _ = w.Write([]byte(`{"id":1,"status":"accepted"}`))
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	r := &Reporter{URL: srv.URL + "/api/v1/argus/reviews", Token: "tok-1"}
	pr := &plugin.PRContext{Platform: "gitea", Owner: "z", Repo: "d", Number: "1", HeadSHA: "h"}
	p := BuildPayload("task-9", pr, sampleFindings(), "approve", []string{"ocr"}, 100, 1, time.Now())
	r.report(p) // 直调内部同步版本，绕过 goroutine

	if calls.Load() != 2 {
		t.Fatalf("500 后应重试一次，got %d 次", calls.Load())
	}
	if gotAuth != "Bearer tok-1" {
		t.Fatalf("应带 Bearer 凭证: %q", gotAuth)
	}
	var back Payload
	if err := json.Unmarshal([]byte(gotBody), &back); err != nil {
		t.Fatalf("对端应收到合法契约 JSON: %v", err)
	}
	if back.ArgusTaskID != "task-9" || len(back.Findings) != 2 {
		t.Fatalf("对端收到的契约不符: %+v", back)
	}
}

// 出站守卫走可信档：配置面 URL 指向回环承接端点在 warn/enforce 下均可达
// （元数据/链路本地仍由拨号守卫拒绝，不在本用例制造真实拨号）。
func TestReportLoopbackViaTrustedClient(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(`{"id":1,"status":"accepted"}`))
	}))
	t.Cleanup(srv.Close)

	lg := &recordLogger{Logger: logx.Nop()}
	r := &Reporter{URL: srv.URL + "/api/v1/argus/reviews", Log: lg}
	r.report(&Payload{ArgusTaskID: "task-loop", OccurredAt: time.Now()})

	if calls.Load() != 1 {
		t.Fatalf("回环承接端点应收到 1 次上报，got %d", calls.Load())
	}
	if lg.warnCount() != 0 {
		t.Fatalf("可信档应放行回环，不应有上报失败告警: %v", lg.warns)
	}
}

// 未配置（nil / 空 URL）时静默跳过。
func TestReportDisabled(t *testing.T) {
	var nilRep *Reporter
	if nilRep.Enabled() {
		t.Fatal("nil reporter 应为关闭")
	}
	(&Reporter{}).Report(nil) // 空 URL：不发请求、不 panic
}

// 上报客户端必须挂可信档出站守卫：逐跳判据硬拒元数据/链路本地，内网/回环
// 放行（可信档特征）；全程只调 CheckRedirect，不发起真实网络调用。
func TestNewHTTPClientGuardActive(t *testing.T) {
	c := newHTTPClient()
	if c.CheckRedirect == nil {
		t.Fatal("上报客户端必须挂出站逐跳守卫（CheckRedirect 为空 = 守卫旁路）")
	}
	via := []*http.Request{httptest.NewRequest(http.MethodPost, "http://127.0.0.1/", nil)}
	// 可信档特征：内网/回环承接端点放行（与半可信档的唯一差别）。
	for _, raw := range []string{
		"http://127.0.0.1:8000/api/v1/argus/reviews",
		"http://10.1.2.3:8000/api/v1/argus/reviews",
	} {
		if err := c.CheckRedirect(httptest.NewRequest(http.MethodPost, raw, nil), via); err != nil {
			t.Fatalf("可信档应放行内网/回环 %s: %v", raw, err)
		}
	}
	// 任何信任档都硬拒：云元数据与链路本地。
	for _, raw := range []string{
		"http://169.254.169.254/latest/meta-data",
		"http://[fd00:ec2::254]/",
		"http://[fe80::1]/",
		"http://metadata.google.internal/",
	} {
		if err := c.CheckRedirect(httptest.NewRequest(http.MethodPost, raw, nil), via); err == nil {
			t.Fatalf("元数据/链路本地 %s 必须被上报客户端拒绝", raw)
		}
	}
}
