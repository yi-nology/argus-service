package execx_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/oekit/logx"

	"github.com/yi-nology/argus/internal/execpolicy"
	"github.com/yi-nology/argus/internal/execx"
)

// configurePolicy 设置执行策略并在测试结束复位为 warn（避免串扰其他用例）。
func configurePolicy(t *testing.T, mode execpolicy.Mode) {
	t.Helper()
	execpolicy.Configure(mode, logx.Nop())
	t.Cleanup(func() { execpolicy.Configure(execpolicy.ModeWarn, logx.Nop()) })
}

// enforce：危险命令返回 ExitCode=-1 的 *execx.Error，错误链含 ErrDenied。
func TestRunEnforceDeniesDangerousCommands(t *testing.T) {
	configurePolicy(t, execpolicy.ModeEnforce)

	cases := [][]string{
		{"sh", "-c", "rm -rf /tmp/argus-execpolicy-never-created"},
		{"/bin/sh", "-c", "git status && curl https://example.com"},
		{"sh", "script.sh"},
		{"rm", "-rf", "/tmp/argus-execpolicy-never-created"},
		{"/bin/rm", "-rf", "/tmp/argus-execpolicy-never-created"},
		{"curl", "https://example.com"},
	}
	for _, argv := range cases {
		stdout, stderr, err := execx.Run(context.Background(), argv[0], argv[1:])
		if err == nil {
			t.Errorf("Run(%q) 应被策略拒绝", argv)
			continue
		}
		if !errors.Is(err, execpolicy.ErrDenied) {
			t.Errorf("Run(%q) 错误链缺少 execpolicy.ErrDenied: %v", argv, err)
		}
		var ee *execx.Error
		if !errors.As(err, &ee) {
			t.Errorf("Run(%q) 错误类型 = %T，期望 *execx.Error", argv, err)
			continue
		}
		if ee.ExitCode != -1 {
			t.Errorf("Run(%q) ExitCode = %d，期望 -1", argv, ee.ExitCode)
		}
		if !strings.Contains(err.Error(), argv[0]) {
			t.Errorf("错误文案缺少命令名 %q: %v", argv[0], err)
		}
		if len(stdout) != 0 || len(stderr) != 0 {
			t.Errorf("拒绝路径不应有输出: stdout=%q stderr=%q", stdout, stderr)
		}
	}
}

// enforce：普通命令、git 与内层安全的 `sh -c` 放行（修复验证/测试生成路径）。
func TestRunEnforceAllowsOrdinaryCommands(t *testing.T) {
	configurePolicy(t, execpolicy.ModeEnforce)

	stdout, _, err := execx.Run(context.Background(), "printf", []string{"hi"})
	if err != nil || string(stdout) != "hi" {
		t.Fatalf("printf: out=%q err=%v", stdout, err)
	}
	stdout, _, err = execx.Run(context.Background(), "git", []string{"--version"})
	if err != nil {
		t.Fatalf("git --version 应放行: %v", err)
	}
	if !strings.Contains(string(stdout), "git version") {
		t.Fatalf("git 输出 = %q", stdout)
	}
	stdout, _, err = execx.Run(context.Background(), "sh", []string{"-c", "printf shell-inner-ok"})
	if err != nil || string(stdout) != "shell-inner-ok" {
		t.Fatalf("内层安全的 sh -c 应放行: out=%q err=%v", stdout, err)
	}
}

// WithShell 展开成 /bin/sh -c：内层安全命令在 enforce 放行，危险命令拒绝；
// warn 下均放行。
func TestRunWithShellPolicy(t *testing.T) {
	configurePolicy(t, execpolicy.ModeEnforce)
	stdout, _, err := execx.Run(context.Background(), "printf shell-ok", nil, execx.WithShell())
	if err != nil || string(stdout) != "shell-ok" {
		t.Fatalf("enforce 下 WithShell 安全命令应放行: out=%q err=%v", stdout, err)
	}
	if _, _, err := execx.Run(context.Background(), "rm -rf /tmp/argus-execpolicy-never-created", nil, execx.WithShell()); !errors.Is(err, execpolicy.ErrDenied) {
		t.Fatalf("enforce 下 WithShell 危险命令应被拒绝: %v", err)
	}

	configurePolicy(t, execpolicy.ModeWarn)
	stdout, _, err = execx.Run(context.Background(), "printf shell-ok", nil, execx.WithShell())
	if err != nil || string(stdout) != "shell-ok" {
		t.Fatalf("warn 下 WithShell 应放行: out=%q err=%v", stdout, err)
	}
}

// ProcessRun / ProcessRunDetailed 同样过命令判据；策略错误统一为 *execx.Error。
func TestProcessRunEnforceDenied(t *testing.T) {
	configurePolicy(t, execpolicy.ModeEnforce)

	stdout, stderr, code, err := execx.ProcessRun(context.Background(), execx.ProcessRequest{
		Argv: []string{"sh", "-c", "rm -rf /tmp/argus-execpolicy-never-created"},
	})
	if !errors.Is(err, execpolicy.ErrDenied) {
		t.Fatalf("ProcessRun 错误链缺少 ErrDenied: %v", err)
	}
	var ee *execx.Error
	if !errors.As(err, &ee) {
		t.Fatalf("ProcessRun 错误类型 = %T，期望 *execx.Error", err)
	}
	if ee.Command != "sh" || ee.ExitCode != -1 {
		t.Fatalf("ProcessRun *Error = %+v", ee)
	}
	if code != -1 || stdout != "" || stderr != "" {
		t.Fatalf("ProcessRun 拒绝路径: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}

	res, err := execx.ProcessRunDetailed(context.Background(), execx.ProcessRequest{
		Argv: []string{"rm", "-rf", "/tmp/argus-execpolicy-never-created"},
	})
	if !errors.Is(err, execpolicy.ErrDenied) {
		t.Fatalf("ProcessRunDetailed 错误链缺少 ErrDenied: %v", err)
	}
	if !errors.As(err, &ee) || ee.Command != "rm" || ee.ExitCode != -1 {
		t.Fatalf("ProcessRunDetailed *Error = %+v", ee)
	}
	if res.ExitCode != -1 {
		t.Fatalf("ProcessRunDetailed ExitCode = %d，期望 -1", res.ExitCode)
	}

	// 内层安全的 sh -c 在 enforce 下正常执行（验证/测试生成的真实路径）。
	stdout, _, code, err = execx.ProcessRun(context.Background(), execx.ProcessRequest{
		Argv: []string{"sh", "-c", "printf process-inner-ok"},
	})
	if err != nil || code != 0 || stdout != "process-inner-ok" {
		t.Fatalf("ProcessRun 内层安全命令应放行: out=%q code=%d err=%v", stdout, code, err)
	}
}

// 显式 env：危险键剥离，普通键保留（warn 模式也剥离）。
func TestRunStripsDangerousEnvFromExplicitEnv(t *testing.T) {
	configurePolicy(t, execpolicy.ModeWarn)

	stdout, _, err := execx.Run(context.Background(), "env", nil, execx.WithEnv([]string{
		"LD_PRELOAD=/tmp/argus-evil.so",
		"DYLD_INSERT_LIBRARIES=/tmp/argus-evil.dylib",
		"GIT_SSH_COMMAND=/tmp/argus-evil.sh",
		"ARGUS_EXECX_KEEP=keep",
	}))
	if err != nil {
		t.Fatalf("Run(env): %v", err)
	}
	out := string(stdout)
	if !strings.Contains(out, "ARGUS_EXECX_KEEP=keep") {
		t.Fatalf("普通变量未透传: %q", out)
	}
	for _, bad := range []string{"LD_PRELOAD=", "DYLD_INSERT_LIBRARIES=", "GIT_SSH_COMMAND="} {
		if strings.Contains(out, bad) {
			t.Fatalf("子进程仍见到危险变量 %s: %q", bad, out)
		}
	}
}

// nil env 等价继承 os.Environ()，但仍经过剥离器。
func TestRunStripsDangerousEnvFromInheritedEnv(t *testing.T) {
	configurePolicy(t, execpolicy.ModeWarn)
	t.Setenv("LD_PRELOAD", "/tmp/argus-evil.so")
	t.Setenv("ARGUS_EXECX_INHERIT", "keep")

	stdout, _, err := execx.Run(context.Background(), "env", nil)
	if err != nil {
		t.Fatalf("Run(env): %v", err)
	}
	out := string(stdout)
	if !strings.Contains(out, "ARGUS_EXECX_INHERIT=keep") {
		t.Fatalf("继承环境未生效: %q", out)
	}
	if strings.Contains(out, "LD_PRELOAD=") {
		t.Fatalf("继承路径未剥离 LD_PRELOAD: %q", out)
	}
}

// ProcessRun 的 req.Env 同样过剥离器。
func TestProcessRunStripsDangerousEnv(t *testing.T) {
	configurePolicy(t, execpolicy.ModeWarn)

	stdout, _, code, err := execx.ProcessRun(context.Background(), execx.ProcessRequest{
		Argv: []string{"env"},
		Env:  []string{"LD_PRELOAD=/tmp/argus-evil.so", "ARGUS_EXECX_KEEP=keep"},
	})
	if err != nil {
		t.Fatalf("ProcessRun(env): %v", err)
	}
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if !strings.Contains(stdout, "ARGUS_EXECX_KEEP=keep") {
		t.Fatalf("普通变量未透传: %q", stdout)
	}
	if strings.Contains(stdout, "LD_PRELOAD=") {
		t.Fatalf("子进程仍见到 LD_PRELOAD: %q", stdout)
	}
}
