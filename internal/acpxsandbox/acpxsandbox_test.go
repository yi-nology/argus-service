package acpxsandbox

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/oagentkit/guardx/sandbox"
	"github.com/oagentkit/tools/acpx"

	"github.com/yi-nology/goagent-kernel/egress"
	"github.com/yi-nology/goagent-kernel/gatecheck"

	"github.com/yi-nology/argus/internal/execpolicy"
)

// stubAgent 假 acpx agent：记录收到的 RunRequest，返回固定结果。
type stubAgent struct {
	name string
	text string
	err  error
	code int
	got  acpx.RunRequest
	n    int
}

func (s *stubAgent) Name() string { return s.name }

func (s *stubAgent) Run(_ context.Context, req acpx.RunRequest) (*acpx.RunResult, error) {
	s.got = req
	s.n++
	if s.err != nil {
		return nil, s.err
	}
	return &acpx.RunResult{Text: s.text, ExitCode: s.code, Stderr: "stderr-tail"}, nil
}

func (s *stubAgent) Capabilities() acpx.Capability {
	return acpx.Capability{Sandbox: true}
}

// resetSettings 把全局出站声明恢复成缺省 unrestricted，并在用例结束后复位。
func resetSettings(t *testing.T) {
	t.Helper()
	Configure("", "", nil, nil, nil)
	t.Cleanup(func() { Configure("", "", nil, nil, nil) })
}

func mustGuard(t *testing.T, agent acpx.Agent, profile Profile, bin string) *Guard {
	t.Helper()
	g, err := New(Config{Agent: agent, Profile: profile, Bin: bin})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return g
}

func TestParseProfile(t *testing.T) {
	cases := map[string]Profile{
		"":         ProfileReadonly,
		"readonly": ProfileReadonly,
		" write ":  ProfileWrite,
		"NETWORK":  ProfileNetwork,
		"ReAdOnLy": ProfileReadonly,
	}
	for raw, want := range cases {
		got, err := ParseProfile(raw)
		if err != nil || got != want {
			t.Fatalf("ParseProfile(%q) = %q, %v；期望 %q", raw, got, err, want)
		}
	}
	if _, err := ParseProfile("yolo"); err == nil {
		t.Fatal("未知名应报错（不回落缺省）")
	}
}

// 档位矩阵：provider-native 文件系统档与 kernel 预设三轴。
func TestProfileSandboxMatrix(t *testing.T) {
	cases := []struct {
		profile Profile
		sandbox string
		mode    string
	}{
		{ProfileReadonly, acpx.SandboxReadonly, "confirm"},
		{ProfileWrite, acpx.SandboxWorkspace, "auto"},
		{ProfileNetwork, acpx.SandboxFull, "full"},
	}
	for _, tc := range cases {
		if got := tc.profile.Sandbox(); got != tc.sandbox {
			t.Fatalf("%s Sandbox = %q，期望 %q", tc.profile, got, tc.sandbox)
		}
		p := tc.profile.Preset(egress.Spec{})
		if p.Level != sandbox.LevelProcess {
			t.Fatalf("%s Level = %s，期望 LevelProcess", tc.profile, p.Level)
		}
		if string(p.Mode) != tc.mode {
			t.Fatalf("%s Mode = %q，期望 %q", tc.profile, p.Mode, tc.mode)
		}
	}
}

func TestNewRejectsBadConfig(t *testing.T) {
	resetSettings(t)
	if _, err := New(Config{}); err == nil {
		t.Fatal("nil agent 应报错")
	}
	if _, err := New(Config{Agent: &stubAgent{name: "s"}, Profile: "yolo"}); err == nil {
		t.Fatal("未知档位应报错")
	}
	Configure("bogus", "", nil, nil, nil)
	if _, err := New(Config{Agent: &stubAgent{name: "s"}}); err == nil {
		t.Fatal("非法出站配置应在装配期报错")
	}
	Configure("", "", nil, nil, nil)
}

func TestRunReadonly(t *testing.T) {
	resetSettings(t)
	sa := &stubAgent{name: "stub", text: "findings"}
	g := mustGuard(t, sa, ProfileReadonly, "stub")

	res, err := g.Run(context.Background(), Request{Prompt: "review this", WorkDir: "/tmp/wd", Timeout: time.Minute})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Text != "findings" {
		t.Fatalf("Text = %q", res.Text)
	}
	if sa.n != 1 {
		t.Fatalf("agent 调用次数 = %d", sa.n)
	}
	if sa.got.Sandbox != acpx.SandboxReadonly {
		t.Fatalf("Sandbox = %q，期望 readonly", sa.got.Sandbox)
	}
	if sa.got.WorkDir != "/tmp/wd" || sa.got.Prompt != "review this" {
		t.Fatalf("请求字段未透传: %+v", sa.got)
	}
	if sa.got.Timeout != time.Minute {
		t.Fatalf("Timeout = %v", sa.got.Timeout)
	}
}

func TestRunWriteAndNetworkProfiles(t *testing.T) {
	resetSettings(t)
	for _, tc := range []struct {
		profile Profile
		want    string
	}{
		{ProfileWrite, acpx.SandboxWorkspace},
		{ProfileNetwork, acpx.SandboxFull},
	} {
		sa := &stubAgent{name: "stub", text: "ok"}
		g := mustGuard(t, sa, tc.profile, "stub")
		if _, err := g.Run(context.Background(), Request{Prompt: "p"}); err != nil {
			t.Fatalf("%s Run: %v", tc.profile, err)
		}
		if sa.got.Sandbox != tc.want {
			t.Fatalf("%s Sandbox = %q，期望 %q", tc.profile, sa.got.Sandbox, tc.want)
		}
	}
}

func TestRunEmptyPromptRefused(t *testing.T) {
	resetSettings(t)
	sa := &stubAgent{name: "stub", text: "x"}
	g := mustGuard(t, sa, ProfileReadonly, "stub")
	if _, err := g.Run(context.Background(), Request{Prompt: "  "}); !errors.Is(err, ErrRefused) {
		t.Fatalf("空 prompt 应拒绝，得到 %v", err)
	}
	if sa.n != 0 {
		t.Fatal("拒绝时不得启动 agent")
	}
}

func TestRunAgentErrorPropagates(t *testing.T) {
	resetSettings(t)
	boom := errors.New("boom")
	sa := &stubAgent{name: "stub", err: boom}
	g := mustGuard(t, sa, ProfileReadonly, "stub")
	_, err := g.Run(context.Background(), Request{Prompt: "p"})
	if !errors.Is(err, boom) {
		t.Fatalf("作业失败应原样上抛（可 errors.Is），得到 %v", err)
	}
	if sa.n != 1 {
		t.Fatalf("agent 调用次数 = %d", sa.n)
	}
}

// egress 关闭（denied）：没有禁网强制后端 ⇒ 不执行（fail closed）。
func TestEgressDeniedRefused(t *testing.T) {
	resetSettings(t)
	Configure("denied", "", nil, nil, nil)
	t.Cleanup(func() { Configure("", "", nil, nil, nil) })

	sa := &stubAgent{name: "stub", text: "x"}
	g, err := New(Config{Agent: sa, Profile: ProfileReadonly})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = g.Run(context.Background(), Request{Prompt: "p"})
	if !errors.Is(err, ErrRefused) {
		t.Fatalf("denied 应拒绝执行，得到 %v", err)
	}
	if !strings.Contains(err.Error(), "出站策略拒绝") {
		t.Fatalf("拒绝理由应点名出站策略: %v", err)
	}
	if sa.n != 0 {
		t.Fatal("拒绝时不得启动 agent")
	}
}

// proxy 要求：env 能生成，但没有能强制代理的后端 ⇒ gatecheck 判 egress_short。
func TestEgressProxyRefusedWithoutEnforcer(t *testing.T) {
	resetSettings(t)
	Configure("proxy", "http://127.0.0.1:3128", nil, nil, nil)
	t.Cleanup(func() { Configure("", "", nil, nil, nil) })

	sa := &stubAgent{name: "stub", text: "x"}
	g, err := New(Config{Agent: sa, Profile: ProfileWrite})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if g.Egress().Mode != egress.ModeProxy {
		t.Fatalf("Egress = %s", g.Egress().Mode)
	}
	_, err = g.Run(context.Background(), Request{Prompt: "p"})
	if !errors.Is(err, ErrRefused) {
		t.Fatalf("proxy 无强制后端应拒绝，得到 %v", err)
	}
	if !strings.Contains(err.Error(), "egress_short") {
		t.Fatalf("拒绝理由应点名 egress_short: %v", err)
	}
	if sa.n != 0 {
		t.Fatal("拒绝时不得启动 agent")
	}
}

func TestCmdPolicyRejectsDangerousBinary(t *testing.T) {
	resetSettings(t)
	execpolicy.Configure(execpolicy.ModeEnforce, nil)
	t.Cleanup(func() { execpolicy.Configure(execpolicy.ModeWarn, nil) })

	sa := &stubAgent{name: "rm", text: "x"}
	g := mustGuard(t, sa, ProfileReadonly, "rm")
	_, err := g.Run(context.Background(), Request{Prompt: "p"})
	if !errors.Is(err, ErrRefused) || !errors.Is(err, execpolicy.ErrDenied) {
		t.Fatalf("应被 cmdpolicy 拒绝（双哨兵可判），得到 %v", err)
	}
	if sa.n != 0 {
		t.Fatal("拒绝时不得启动 agent")
	}
}

func TestCheckDecision(t *testing.T) {
	resetSettings(t)
	g := mustGuard(t, &stubAgent{name: "stub"}, ProfileReadonly, "stub")
	dec, err := g.Check(context.Background(), Request{Prompt: "p"})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if dec.Outcome != gatecheck.OutcomeProceed {
		t.Fatalf("Outcome = %s（%s）", dec.Outcome, dec.Reason)
	}
	if dec.Provider == nil {
		t.Fatal("放行必须带可执行后端")
	}
}
