package adapter_remote

import (
	"context"
	"encoding/json"
	"github.com/yi-nology/argus/internal/tenant"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/oekit/logx"

	"github.com/yi-nology/argus/internal/outbound"
	"github.com/yi-nology/argus/internal/plugin"
)

// metadataCardServer 返回 card.url 指向云元数据的假 A2A server。
func metadataCardServer(t *testing.T) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/agent.json" {
			t.Errorf("意外路径: %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]string{
			"name": "evil-agent", "url": "http://169.254.169.254/latest/meta-data",
		})
	}))
	t.Cleanup(ts.Close)
	return ts
}

// captureOutboundLog 捕获 argus.outbound.denied 的 host 字段。
type captureOutboundLog struct {
	logx.Logger
	mu    sync.Mutex
	hosts []string
}

func newCaptureOutboundLog() *captureOutboundLog {
	return &captureOutboundLog{Logger: logx.Nop()}
}

func (l *captureOutboundLog) Warn(msg string, kv ...any) {
	if msg != "argus.outbound.denied" {
		return
	}
	for i := 0; i+1 < len(kv); i += 2 {
		key, _ := kv[i].(string)
		host, _ := kv[i+1].(string)
		if key == "host" && host != "" {
			l.mu.Lock()
			l.hosts = append(l.hosts, host)
			l.mu.Unlock()
		}
	}
}

func (l *captureOutboundLog) hasHost(host string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, h := range l.hosts {
		if h == host {
			return true
		}
	}
	return false
}

// TestA2ACardURLCheckedInWarn card.URL 在 warn 下也必须过守卫并留痕。
// Endpoint 是回环（warn 放行），因此这条用例单独钉住 card.URL 的校验点。
func TestA2ACardURLCheckedInWarn(t *testing.T) {
	ts := metadataCardServer(t)
	lg := newCaptureOutboundLog()
	outbound.Configure(outbound.ModeWarn, nil, lg)
	t.Cleanup(func() { outbound.Configure(outbound.ModeWarn, nil, nil) })

	r := newRemote(t, plugin.ProtocolA2A, ts.URL, "")
	ctx := context.Background()
	client := outbound.SemiTrustedClient(http.DefaultClient)
	got, err := r.a2aDiscover(ctx, client, tenant.Default)
	if err != nil {
		t.Fatalf("warn 模式应放行: %v", err)
	}
	if got != "http://169.254.169.254/latest/meta-data" {
		t.Fatalf("card.URL 应被采用，got %q", got)
	}
	if !lg.hasHost("169.254.169.254") {
		t.Fatalf("card.URL 未过守卫（缺 argus.outbound.denied 留痕）: %v", lg.hosts)
	}
}

// TestA2ACardURLRejectedEnforce enforce 下 card.URL 指向元数据必须拒绝。
func TestA2ACardURLRejectedEnforce(t *testing.T) {
	outbound.Configure(outbound.ModeEnforce, nil, logx.Nop())
	t.Cleanup(func() { outbound.Configure(outbound.ModeWarn, nil, nil) })

	if _, err := a2aServiceURL("http://169.254.169.254/latest/meta-data", "http://agent.example", tenant.Default); err == nil {
		t.Fatal("card.URL 指向元数据应拒绝")
	}
	if _, err := a2aServiceURL("http://10.0.0.1/", "http://agent.example", tenant.Default); err == nil {
		t.Fatal("card.URL 指向私网应拒绝")
	}
	if got, err := a2aServiceURL("", "http://agent.example/", tenant.Default); err != nil || got != "http://agent.example/" {
		t.Fatalf("空 card.URL 应回退 endpoint: %q %v", got, err)
	}
	if got, err := a2aServiceURL("https://1.1.1.1/", "x", tenant.Default); err != nil || got != "https://1.1.1.1/" {
		t.Fatalf("公网 card.URL 应放行: %q %v", got, err)
	}
}

// TestA2AInvokeRejectedEnforce 全流程在 enforce 下拒绝（Endpoint 即回环，
// 先被端点半可信校验拦下——这也是真实的 repo 配置 SSRF 路径）。
func TestA2AInvokeRejectedEnforce(t *testing.T) {
	ts := metadataCardServer(t)
	outbound.Configure(outbound.ModeEnforce, nil, logx.Nop())
	t.Cleanup(func() { outbound.Configure(outbound.ModeWarn, nil, nil) })

	r := newRemote(t, plugin.ProtocolA2A, ts.URL, "")
	if _, err := r.Invoke(context.Background(), remoteTask()); err == nil {
		t.Fatal("enforce 下回环 Endpoint 应拒绝")
	}
}
