package sink

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yi-nology/argus/internal/domain"
	"github.com/yi-nology/argus/internal/outbound"
	"github.com/yi-nology/argus/internal/plugin"
)

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

// 未配置（nil / 空 URL）时静默跳过。
func TestReportDisabled(t *testing.T) {
	var nilRep *Reporter
	if nilRep.Enabled() {
		t.Fatal("nil reporter 应为关闭")
	}
	(&Reporter{}).Report(nil) // 空 URL：不发请求、不 panic
}

// ── #44/#45 回归：出站守卫 ──

// NewReporter 缺省客户端 = outbound.TrustedClient（可信档）：httptest 回环自建
// 服务照常可达（守卫不误伤合法端点），凭证与契约逐字到端。
func TestReportUsesGuardedTrustedClient(t *testing.T) {
	var calls atomic.Int32
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"id":1,"status":"accepted"}`))
	}))
	t.Cleanup(srv.Close)

	r := NewReporter(srv.URL, "tok-2", "", nil) // egressMode 空 = enforce（保守归一）
	if r.Mode != outbound.ModeEnforce {
		t.Fatalf("空词表应归一 enforce，got %q", r.Mode)
	}
	p := BuildPayload("task-44", &plugin.PRContext{Platform: "gitea", Owner: "z", Repo: "d", Number: "1", HeadSHA: "h"},
		sampleFindings(), "approve", nil, 0, 0, time.Now())
	r.report(p) // 直调同步版

	if calls.Load() != 1 {
		t.Fatalf("可信档客户端应直达回环端点，got %d 次", calls.Load())
	}
	if gotAuth != "Bearer tok-2" {
		t.Fatalf("应带 Bearer 凭证: %q", gotAuth)
	}
}

// enforce 档 SSRF 负例：URL 指向云元数据段（恒拒段，不受可信档放行面影响）——
// 拨号层预检即拦，零拨号、零请求，只走既有「上报失败只告警」旁路。
func TestReportSSRFEnforceBlocks(t *testing.T) {
	d := &countingDialer{}
	r := NewReporter("http://169.254.169.254:8000/api/v1/argus/reviews", "", "enforce", nil)
	r.Client, _ = outbound.TrustedClient(nil, outbound.Options{Dial: d.DialContext})
	p := BuildPayload("task-ssrf", &plugin.PRContext{}, sampleFindings(), "approve", nil, 0, 0, time.Now())
	r.report(p)

	if n := d.dials.Load(); n != 0 {
		t.Fatalf("元数据段应预检即拦零拨号，got %d", n)
	}
}

// warn 影子档（ARGUS_SINK_EGRESS=warn）：不可信面下回环属违规，留痕后放行——
// 行为与未接守卫一致，只多影子告警（灰度期实测拦截面）。
func TestReportSSRFWarnShadowProceeds(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(`{"id":1,"status":"accepted"}`))
	}))
	t.Cleanup(srv.Close)

	var shadow atomic.Int32
	d := &countingDialer{}
	r := NewReporter(srv.URL, "", "warn", nil)
	// 刻意取不可信面（outbound.Client 零值 Allow = 只放公网）：回环违规 → 影子
	// 留痕。不用 TrustedClient——它会把零值放行面补成可信档（回环合法），
	// 测不出影子行为。
	r.Client, _ = outbound.Client(nil, outbound.Options{
		Mode:        outbound.ModeWarn,
		Dial:        d.DialContext,
		OnViolation: func(string) { shadow.Add(1) },
	})
	p := BuildPayload("task-warn", &plugin.PRContext{}, sampleFindings(), "approve", nil, 0, 0, time.Now())
	r.report(p)

	if calls.Load() != 1 {
		t.Fatalf("影子档应放行上报，got %d 次", calls.Load())
	}
	// 两道闸各自留痕：字面量预检 + 建连后 RemoteAddr 复核（同一回环地址，两处
	// 判定都看见它）——影子档按闸留痕而非按请求去重。
	if shadow.Load() != 2 {
		t.Fatalf("回环违规应影子留痕恰 2 次（预检 + RemoteAddr 复核），got %d", shadow.Load())
	}
}

// countingDialer 真拨号计数器：断言「守卫拦下 = 零拨号」的支点（放行路径仍走
// 真网络，httptest 端点真实收到请求）。
type countingDialer struct {
	real  net.Dialer
	dials atomic.Int32
}

func (d *countingDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	d.dials.Add(1)
	return d.real.DialContext(ctx, network, addr)
}
