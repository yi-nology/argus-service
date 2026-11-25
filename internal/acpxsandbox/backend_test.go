package acpxsandbox

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/oagentkit/guardx/sandbox"

	kernelbackend "github.com/oagentkit/goagent-kernel/backend"
	"github.com/oagentkit/goagent-kernel/egress"
)

func TestBackendContract(t *testing.T) {
	sa := &stubAgent{name: "stub", text: "ok"}
	b := NewBackend(sa, ProfileReadonly)
	if b.ID() != BackendID(ProfileReadonly) {
		t.Fatalf("ID = %q", b.ID())
	}
	if b.Isolation() != kernelbackend.IsolationProcess {
		t.Fatalf("Isolation = %s", b.Isolation())
	}
	if b.Egress() != sandbox.EgressUnrestricted {
		t.Fatalf("Egress = %s", b.Egress())
	}
	if b.Capable(sandbox.Budget{MaxMemory: 1}) {
		t.Fatal("内存预算不应声明可兑现")
	}
	if b.Capable(sandbox.Budget{MaxCalls: 1}) || b.Capable(sandbox.Budget{MaxBytes: 1}) {
		t.Fatal("调用数/字节预算不应声明可兑现")
	}
	if !b.Capable(sandbox.Budget{WallClock: time.Second}) {
		t.Fatal("墙钟应声明可兑现")
	}

	// 合法作业 → 真实执行。
	res, err := b.Exec(context.Background(), kernelbackend.Job{
		Image: "stub", Args: []string{"prompt"}, WorkDir: "/tmp/wd",
		Budget: sandbox.Budget{WallClock: time.Second},
	})
	if err != nil || !res.OK || res.Stdout != "ok" {
		t.Fatalf("Exec = %+v, %v", res, err)
	}
	// 失败作业：error 通道为空，OK=false 携带退出码/stderr。
	bad := &stubAgent{name: "stub", err: errors.New("exit 1")}
	bres, err := NewBackend(bad, ProfileReadonly).Exec(context.Background(),
		kernelbackend.Job{Image: "stub", Args: []string{"p"}})
	if err != nil {
		t.Fatalf("作业失败不应走 error 通道: %v", err)
	}
	if bres.OK || bres.ExitCode != -1 {
		t.Fatalf("失败结果 = %+v", bres)
	}
}

func TestBackendRejectsBadJobs(t *testing.T) {
	b := NewBackend(&stubAgent{name: "stub"}, ProfileReadonly)
	cases := []kernelbackend.Job{
		{Image: "other", Args: []string{"p"}},
		{Image: "stub"},
		{Image: "stub", Args: []string{"p"}, Mounts: []kernelbackend.Mount{{Source: "/a", Target: "/b"}}},
		{Image: "stub", Args: []string{"p"}, Env: []string{"BAD"}},
	}
	for i, job := range cases {
		if _, err := b.Exec(context.Background(), job); !errors.Is(err, kernelbackend.ErrBadSpec) {
			t.Fatalf("case %d: err = %v，期望 ErrBadSpec", i, err)
		}
	}
	if _, err := (*Backend)(nil).Exec(context.Background(), kernelbackend.Job{}); err == nil {
		t.Fatal("nil 后端应报错")
	}
}

func TestSpecRejectsMemoryBudget(t *testing.T) {
	b := NewBackend(&stubAgent{name: "stub"}, ProfileReadonly)
	spec := kernelbackend.Spec{
		MinIsolation: kernelbackend.IsolationProcess,
		Budget:       sandbox.Budget{MaxMemory: 1},
	}
	if err := spec.SatisfiedBy(b); err == nil {
		t.Fatal("要求内存兜底时应判不满足")
	}
	proxy := kernelbackend.Spec{MinIsolation: kernelbackend.IsolationProcess, Egress: egress.ModeProxy}
	if err := proxy.SatisfiedBy(b); err == nil {
		t.Fatal("要求代理出口时应判不满足")
	}
}

func TestProviderDeclarations(t *testing.T) {
	p := newProvider(&stubAgent{name: "stub"}, ProfileReadonly, &kernelbackend.Registry{})
	if p.ID() != ProviderID {
		t.Fatalf("ID = %q", p.ID())
	}
	if p.Level() != sandbox.LevelProcess {
		t.Fatalf("Level = %s", p.Level())
	}
	if p.Egress() != sandbox.EgressUnrestricted {
		t.Fatalf("Egress = %s", p.Egress())
	}
	if p.Supports(sandbox.Budget{MaxMemory: 1}) || !p.Supports(sandbox.Budget{WallClock: time.Second}) {
		t.Fatal("Supports 口径与 Backend.Capable 不一致")
	}
}

func TestProviderExecWithoutBackend(t *testing.T) {
	p := newProvider(&stubAgent{name: "stub"}, ProfileReadonly, &kernelbackend.Registry{})
	if _, err := p.Exec(context.Background(), sandbox.Request{Program: "p"}); err == nil {
		t.Fatal("空登记簿应报错（不降级）")
	}
}

func TestEgressSpecEnvAndVerify(t *testing.T) {
	spec := egress.Spec{Mode: egress.ModeProxy, Proxy: "http://127.0.0.1:3128", Bypass: []string{"localhost"}}
	env, err := spec.Env(nil)
	if err != nil {
		t.Fatalf("Env: %v", err)
	}
	m := envToMap(env)
	if m["HTTPS_PROXY"] != spec.Proxy || m["NO_PROXY"] != "localhost" {
		t.Fatalf("proxy env 未生成: %v", m)
	}
	if err := spec.Verify(m); err != nil {
		t.Fatalf("Verify: %v", err)
	}
	// 同名键覆盖：调用方给的旧代理值不得压过声明。
	merged := envToMap(mergeEnv([]string{"HTTPS_PROXY=old", "KEEP=1"}, env))
	if merged["HTTPS_PROXY"] != spec.Proxy || merged["KEEP"] != "1" {
		t.Fatalf("mergeEnv = %v", merged)
	}
}

func TestParseEgressMode(t *testing.T) {
	cases := map[string]egress.Mode{
		"":             egress.ModeUnrestricted,
		"unrestricted": egress.ModeUnrestricted,
		"proxy":        egress.ModeProxy,
		" DENIED ":     egress.ModeDenied,
	}
	for raw, want := range cases {
		got, err := parseEgressMode(raw)
		if err != nil || got != want {
			t.Fatalf("parseEgressMode(%q) = %v, %v", raw, got, err)
		}
	}
	if _, err := parseEgressMode("nope"); err == nil {
		t.Fatal("未知档位应报错")
	}
	if got := splitList(" a, ,b ,, c "); len(got) != 3 || got[0] != "a" || got[2] != "c" {
		t.Fatalf("splitList = %v", got)
	}
}

// ConfigureFromEnv 是部署面的唯一入口：词表/名单解析与非法值都在这条线上。
func TestConfigureFromEnv(t *testing.T) {
	t.Setenv(envEgressMode, "proxy")
	t.Setenv(envEgressProxy, "http://127.0.0.1:3128")
	t.Setenv(envEgressAllow, " api.example.com, ,internal.example.com ")
	t.Setenv(envEgressBypass, "localhost")
	ConfigureFromEnv(nil)
	t.Cleanup(func() { Configure("", "", nil, nil, nil) })

	s := loadSettings()
	if s.err != nil {
		t.Fatalf("配置应合法: %v", s.err)
	}
	if s.spec.Mode != egress.ModeProxy || s.spec.Proxy != "http://127.0.0.1:3128" {
		t.Fatalf("spec = %+v", s.spec)
	}
	if len(s.spec.Allow) != 2 || s.spec.Allow[0] != "api.example.com" {
		t.Fatalf("Allow = %v", s.spec.Allow)
	}
	if len(s.spec.Bypass) != 1 || s.spec.Bypass[0] != "localhost" {
		t.Fatalf("Bypass = %v", s.spec.Bypass)
	}

	// 非法档位：错误保留到 New（fail closed），不回落缺省。
	t.Setenv(envEgressMode, "nope")
	ConfigureFromEnv(nil)
	if loadSettings().err == nil {
		t.Fatal("非法档位应保留配置错误")
	}
}
