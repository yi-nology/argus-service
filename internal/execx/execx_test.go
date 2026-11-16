package execx_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yi-nology/argus/internal/execx"
)

func TestRunSuccess(t *testing.T) {
	stdout, stderr, err := execx.Run(context.Background(), "/bin/sh",
		[]string{"-c", "printf out; printf err >&2"}, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if string(stdout) != "out" || string(stderr) != "err" {
		t.Fatalf("stdout=%q stderr=%q", stdout, stderr)
	}
}

func TestRunNonZeroExit(t *testing.T) {
	stdout, stderr, err := execx.Run(context.Background(), "/bin/sh",
		[]string{"-c", "printf partial; printf boom >&2; exit 7"})
	if err == nil {
		t.Fatal("非零退出应报错")
	}
	if string(stdout) != "partial" || !strings.Contains(string(stderr), "boom") {
		t.Fatalf("stdout=%q stderr=%q", stdout, stderr)
	}
	var ee *execx.Error
	if !errors.As(err, &ee) {
		t.Fatalf("错误类型 = %T，期望 *execx.Error", err)
	}
	if ee.ExitCode != 7 || !strings.Contains(ee.Stderr, "boom") {
		t.Fatalf("ExitCode=%d Stderr=%q", ee.ExitCode, ee.Stderr)
	}
	var xe *exec.ExitError
	if !errors.As(err, &xe) {
		t.Fatal("错误链应保留 *exec.ExitError")
	}
	msg := err.Error()
	if !strings.Contains(msg, "退出码 7") || !strings.Contains(msg, "boom") {
		t.Fatalf("Error() 文案缺少退出码/stderr: %s", msg)
	}
}

func TestRunTimeout(t *testing.T) {
	start := time.Now()
	_, _, err := execx.Run(context.Background(), "/bin/sh",
		[]string{"-c", "sleep 5"}, execx.WithTimeout(50*time.Millisecond))
	if err == nil {
		t.Fatal("超时应报错")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("errors.Is DeadlineExceeded = false: %v", err)
	}
	var ee *execx.Error
	if !errors.As(err, &ee) || ee.ExitCode != -1 {
		t.Fatalf("超时错误应为 ExitCode=-1 的 *execx.Error: %#v", ee)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("超时未及时杀死子进程: %s", elapsed)
	}
}

// 孙进程继承 stdout/stderr 管道时，Run 在 WaitDelay 到点后返回，而不是等
// 孙进程自然退出（issue #46：`sh -c 'sleep 5 & echo hi'` 曾在管道读端无限阻塞）。
func TestRunWaitDelayBoundsGrandchild(t *testing.T) {
	start := time.Now()
	stdout, _, err := execx.Run(context.Background(), "/bin/sh",
		[]string{"-c", "sleep 5 & echo hi"}, execx.WithTimeout(10*time.Second))
	elapsed := time.Since(start)

	if !strings.Contains(string(stdout), "hi") {
		t.Fatalf("孙进程持管道不应吞掉已产生的输出: %q", stdout)
	}
	// WaitDelay=2s；给 2s 余量覆盖 shell/调度开销，但必须明显早于 sleep 5s。
	if elapsed >= 4*time.Second {
		t.Fatalf("Run 未在 WaitDelay 附近返回: %s", elapsed)
	}
	// 正常退出但管道未关 → exec.ErrWaitDelay（错误链须保留，便于调用方归因）。
	if err != nil && !errors.Is(err, exec.ErrWaitDelay) {
		t.Fatalf("WaitDelay 触发应返回 exec.ErrWaitDelay（可 nil），实得 %v", err)
	}
}

func TestRunCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := execx.Run(ctx, "/bin/sh", []string{"-c", "exit 0"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("errors.Is Canceled = false: %v", err)
	}
}

func TestRunOutputLimit(t *testing.T) {
	stdout, stderr, err := execx.Run(context.Background(), "/bin/sh",
		[]string{"-c", "printf 0123456789abcdef; printf 0123456789abcdef >&2"},
		execx.WithMaxOutput(8))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if string(stdout) != "01234567" || string(stderr) != "01234567" {
		t.Fatalf("输出未按上限截断: stdout=%q stderr=%q", stdout, stderr)
	}
}

func TestRunOutputLimitDefaultOnNonPositive(t *testing.T) {
	stdout, _, err := execx.Run(context.Background(), "/bin/sh",
		[]string{"-c", "printf small"}, execx.WithMaxOutput(0))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if string(stdout) != "small" {
		t.Fatalf("默认上限下输出被误截断: %q", stdout)
	}
}

func TestRunWithDir(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "marker.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, err := execx.Run(context.Background(), "/bin/sh",
		[]string{"-c", "test -f marker.txt"}, execx.WithDir(dir))
	if err != nil {
		t.Fatalf("WithDir 未生效: %v", err)
	}
}

func TestRunWithEnv(t *testing.T) {
	stdout, _, err := execx.Run(context.Background(), "/bin/sh",
		[]string{"-c", `printf '%s' "$ARGUS_EXECX_TEST"`},
		execx.WithEnv([]string{"ARGUS_EXECX_TEST=env-ok"}))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if string(stdout) != "env-ok" {
		t.Fatalf("WithEnv 未生效: %q", stdout)
	}
}

func TestRunWithShell(t *testing.T) {
	stdout, _, err := execx.Run(context.Background(), "printf shell-ok", nil, execx.WithShell())
	if err != nil {
		t.Fatalf("Run(WithShell): %v", err)
	}
	if string(stdout) != "shell-ok" {
		t.Fatalf("shell 输出 = %q", stdout)
	}
	stdout, _, err = execx.Run(context.Background(), "printf", []string{"%s", "arg-ok"}, execx.WithShell())
	if err != nil {
		t.Fatalf("Run(WithShell,args): %v", err)
	}
	if string(stdout) != "arg-ok" {
		t.Fatalf("shell 参数拼接输出 = %q", stdout)
	}
}

func TestRunNilContext(t *testing.T) {
	//lint:ignore SA1012 显式验证 nil ctx 兜底分支
	stdout, _, err := execx.Run(nil, "/bin/sh", []string{"-c", "printf ok"})
	if err != nil || string(stdout) != "ok" {
		t.Fatalf("nil ctx: out=%q err=%v", stdout, err)
	}
}

func TestRunNotFound(t *testing.T) {
	_, _, err := execx.Run(context.Background(), "argus-execx-no-such-binary-xyz", nil)
	if err == nil {
		t.Fatal("找不到可执行文件应报错")
	}
	if !errors.Is(err, exec.ErrNotFound) {
		t.Fatalf("errors.Is ErrNotFound = false: %v", err)
	}
	if !strings.Contains(err.Error(), "PATH") {
		t.Fatalf("错误缺少 PATH 线索: %v", err)
	}
}

func TestRunStartFailure(t *testing.T) {
	dir := t.TempDir() // 对目录执行 → 启动失败，且非 ExitError/ErrNotFound
	_, _, err := execx.Run(context.Background(), dir, nil)
	if err == nil {
		t.Fatal("对目录执行应报错")
	}
	var ee *execx.Error
	if !errors.As(err, &ee) || ee.ExitCode != -1 {
		t.Fatalf("启动失败应为 ExitCode=-1 的 *execx.Error: %#v", ee)
	}
}

func TestLookPath(t *testing.T) {
	path, err := execx.LookPath("sh")
	if err != nil || path == "" {
		t.Fatalf("LookPath(sh) = %q, %v", path, err)
	}
	if _, err := execx.LookPath("argus-execx-no-such-binary-xyz"); err == nil {
		t.Fatal("LookPath 未命中应报错")
	} else if !strings.Contains(err.Error(), "PATH") {
		t.Fatalf("LookPath 错误缺少 PATH 线索: %v", err)
	}
}
