package outbound

import (
	"context"
	"errors"
	"net/netip"
	"strings"
	"sync"
	"testing"

	"github.com/oekit/logx"
)

// recordLogger 捕获 Warn 调用（内核 logx 接口嵌入继承其余方法）。
type recordLogger struct {
	logx.Logger
	mu    sync.Mutex
	warns []string
	infos []string
}

func newRecordLogger() *recordLogger { return &recordLogger{Logger: logx.Nop()} }

func (l *recordLogger) Warn(msg string, _ ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.warns = append(l.warns, msg)
}

func (l *recordLogger) Info(msg string, _ ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.infos = append(l.infos, msg)
}

func (l *recordLogger) firstInfo() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.infos) == 0 {
		return ""
	}
	return l.infos[0]
}

func (l *recordLogger) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.warns)
}

func (l *recordLogger) first() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.warns) == 0 {
		return ""
	}
	return l.warns[0]
}

func resetSettings(t *testing.T) {
	t.Helper()
	t.Cleanup(func() { Configure(ModeWarn, nil, nil) })
}

// TestCheckSemiTrusted 半可信判据表：公网放行；私网/回环/链路本地/元数据/
// unspecified 在 enforce 拒绝、warn 放行、off 放行。
func TestCheckSemiTrusted(t *testing.T) {
	cases := []struct {
		name string
		url  string
		deny bool
	}{
		{"public-ipv4-https", "https://1.1.1.1/", false},
		{"public-ipv4-http", "http://8.8.8.8/", false},
		{"loopback-v4", "http://127.0.0.1/", true},
		{"private-10", "http://10.0.0.1/", true},
		{"private-192", "http://192.168.1.1/", true},
		{"metadata-v4", "http://169.254.169.254/latest/meta-data", true},
		{"metadata-v6", "http://[fd00:ec2::254]/", true},
		{"metadata-name", "http://metadata.google.internal/", true},
		{"loopback-v6", "http://[::1]/", true},
		{"link-local-v6", "http://[fe80::1]/", true},
		{"unspecified", "http://0.0.0.0/", true},
		{"file-scheme", "file:///etc/passwd", true},
		{"ftp-scheme", "ftp://1.1.1.1/x", true},
		{"empty-host", "http:///x", true},
		{"invalid-url", "http://[::1", true},
	}
	for _, tc := range cases {
		t.Run(tc.name+"/enforce", func(t *testing.T) {
			resetSettings(t)
			Configure(ModeEnforce, nil, logx.Nop())
			err := CheckSemiTrusted(tc.url)
			if tc.deny && err == nil {
				t.Fatalf("%s 应在 enforce 下拒绝", tc.url)
			}
			if !tc.deny && err != nil {
				t.Fatalf("%s 应放行: %v", tc.url, err)
			}
		})
		t.Run(tc.name+"/warn", func(t *testing.T) {
			resetSettings(t)
			lg := newRecordLogger()
			Configure(ModeWarn, nil, lg)
			if err := CheckSemiTrusted(tc.url); err != nil {
				t.Fatalf("warn 模式必须放行: %v", err)
			}
			if got := lg.count() > 0; got != tc.deny {
				t.Fatalf("warn 记录违规=%v，期望 %v（warns=%v）", got, tc.deny, lg.warns)
			}
			if tc.deny && lg.first() != "argus.outbound.denied" {
				t.Fatalf("违规日志名不符: %q", lg.first())
			}
		})
		t.Run(tc.name+"/off", func(t *testing.T) {
			resetSettings(t)
			lg := newRecordLogger()
			Configure(ModeOff, nil, lg)
			if err := CheckSemiTrusted(tc.url); err != nil {
				t.Fatalf("off 模式必须旁路: %v", err)
			}
			if lg.count() != 0 {
				t.Fatalf("off 模式不应记录违规: %v", lg.warns)
			}
		})
	}
}

// TestCheckSemiTrustedAllowlist 白名单只豁免名字层：公网 host 放行，
// 私网/回环条目在 v1 不放行（brief 明确）。
func TestCheckSemiTrustedAllowlist(t *testing.T) {
	resetSettings(t)
	Configure(ModeEnforce, []string{"1.1.1.1"}, logx.Nop())
	if err := CheckSemiTrusted("http://1.1.1.1/"); err != nil {
		t.Fatalf("白名单公网 host 应放行: %v", err)
	}
	Configure(ModeEnforce, []string{"127.0.0.1", "::1"}, logx.Nop())
	if err := CheckSemiTrusted("http://127.0.0.1/"); err == nil {
		t.Fatal("白名单不得放行回环（v1 不放行私网）")
	}
	if err := CheckSemiTrusted("http://[::1]/"); err == nil {
		t.Fatal("白名单不得放行 IPv6 回环（v1 不放行私网）")
	}
	// localhost 走域名解析路径（resolves 127.0.0.1），仍被地址族复核拒绝。
	Configure(ModeEnforce, []string{"localhost"}, logx.Nop())
	if err := CheckSemiTrusted("http://localhost/"); err == nil {
		t.Fatal("白名单不得放行解析到回环的域名")
	}
}

func TestNormalizeAllow(t *testing.T) {
	got := normalizeAllow([]string{" Example.COM. ", "https://foo.internal:8443/x", "[::1]:443", ""})
	for _, want := range []string{"example.com", "foo.internal", "::1"} {
		if _, ok := got[want]; !ok {
			t.Fatalf("白名单归一化缺 %q: %v", want, got)
		}
	}
	if len(got) != 3 {
		t.Fatalf("白名单条目数不符: %v", got)
	}
	if normalizeAllow(nil) != nil {
		t.Fatal("空白名单应为 nil")
	}
}

func TestConfigureInvalidModeFallsBackWarn(t *testing.T) {
	resetSettings(t)
	lg := newRecordLogger()
	Configure(Mode("bogus"), nil, lg)
	if s := loadSettings(); s.mode != ModeWarn {
		t.Fatalf("非法模式应回退 warn，got %s", s.mode)
	}
	if lg.first() != "argus.outbound.mode_invalid" {
		t.Fatalf("非法模式应记录告警: %v", lg.warns)
	}
}

func TestConfigureFromEnv(t *testing.T) {
	resetSettings(t)
	t.Setenv(envMode, "enforce")
	t.Setenv(envAllow, " example.com , 1.1.1.1 ")
	lg := newRecordLogger()
	ConfigureFromEnv(lg)
	s := loadSettings()
	if s.mode != ModeEnforce || len(s.allow) != 2 {
		t.Fatalf("环境配置不符: mode=%s allow=%v", s.mode, s.allow)
	}
	if lg.firstInfo() != "argus.outbound.configured" {
		t.Fatalf("装配期应记录生效配置: %v", lg.infos)
	}

	t.Setenv(envMode, "BOGUS")
	ConfigureFromEnv(newRecordLogger())
	if loadSettings().mode != ModeWarn {
		t.Fatal("非法模式环境变量应回退 warn")
	}

	t.Setenv(envMode, "")
	t.Setenv(envAllow, "")
	ConfigureFromEnv(logx.Nop())
	if loadSettings().mode != ModeWarn {
		t.Fatal("未配置模式应缺省 warn")
	}
}

func TestHostForLog(t *testing.T) {
	if got := hostForLog("https://Example.COM./path?token=secret"); got != "example.com" {
		t.Fatalf("hostForLog 泄露/归一化失败: %q", got)
	}
	if got := hostForLog("file:///etc/passwd"); got != "file:///etc/passwd" {
		t.Fatalf("无 host 时应回退原串: %q", got)
	}
	long := "file:///" + strings.Repeat("a", 200)
	if got := hostForLog(long); len(got) > 120 {
		t.Fatalf("超长回退未截断: %d", len(got))
	}
}

// TestDefaultModeIsWarn 未 Configure 时缺省 warn（首版不改变线上行为）。
func TestDefaultModeIsWarn(t *testing.T) {
	old := current.Load()
	t.Cleanup(func() { current.Store(old) })
	current.Store(nil)
	if s := loadSettings(); s.mode != ModeWarn {
		t.Fatalf("缺省模式应为 warn，got %s", s.mode)
	}
}

// TestAllowlistResolveFailures 白名单 host 解析失败/为空：fail-closed。
func TestAllowlistResolveFailures(t *testing.T) {
	old := lookupNetIP
	t.Cleanup(func() { lookupNetIP = old })

	lookupNetIP = func(context.Context, string, string) ([]netip.Addr, error) {
		return nil, errors.New("dns failure")
	}
	if err := checkAllowlistedHost("broken.example"); err == nil {
		t.Fatal("解析失败应拒绝")
	}

	lookupNetIP = func(context.Context, string, string) ([]netip.Addr, error) {
		return nil, nil
	}
	if err := checkAllowlistedHost("empty.example"); err == nil {
		t.Fatal("空解析结果应拒绝")
	}
}

func TestNormalizeAllowAllEmpty(t *testing.T) {
	if got := normalizeAllow([]string{" ", ""}); got != nil {
		t.Fatalf("全空条目应返回 nil，got %v", got)
	}
}
