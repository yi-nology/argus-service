// Package execx 是 argus 唯一的子进程执行出口：os/exec 只允许出现在本包
// （architecture-policy.yaml 的 argus-exec-single-entry 保证）。
//
// 设计要点：
//   - 默认不经 shell，参数按 argv 传递（注入面最小）。
//   - WithShell 才走 /bin/sh -c：命令串由调用方负责引号（本包不做转义，
//     避免给出"看似安全"的假象）；只有确需管道/重定向时才使用。
//   - stdout/stderr 有界（WithMaxOutput，缺省 DefaultMaxOutput），超限截断，
//     防止异常子进程大输出打爆内存。
//   - 非零退出统一返回 *Error（含 ExitCode/Stderr）；错误链保留底层
//     *exec.ExitError，超时链保留 context.DeadlineExceeded（errors.Is 可判）。
package execx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// DefaultMaxOutput 是单路（stdout 或 stderr）输出的缺省上限。
const DefaultMaxOutput = 4 << 20 // 4 MiB

// waitDelay 子进程退出后等待 I/O 管道关闭的上限（等价 exec.Cmd.WaitDelay）。
// 孙进程继承 stdout/stderr 管道时，读端会一直等 EOF；WaitDelay 到点关闭管道，
// 避免 Run 被不在本进程树托管内的孙进程无限拖住。
const waitDelay = 2 * time.Second

// shellPath 是非登录 shell 的绝对路径；WithShell 只走这一条。
const shellPath = "/bin/sh"

// Option 调整单次执行的配置。
type Option func(*config)

type config struct {
	dir       string
	env       []string
	timeout   time.Duration
	maxOutput int
	shell     bool
}

// WithDir 设置子进程工作目录（等价 exec.Cmd.Dir）。
func WithDir(dir string) Option {
	return func(c *config) { c.dir = dir }
}

// WithEnv 整体替换子进程环境（等价 exec.Cmd.Env）；需要继承时由调用方
// 传入 os.Environ()。nil 表示继承当前进程环境。
func WithEnv(env []string) Option {
	return func(c *config) { c.env = env }
}

// WithTimeout 给本次执行附加超时；超时后子进程被杀死，返回的 *Error 的
// Err 链指向 context.DeadlineExceeded。
func WithTimeout(d time.Duration) Option {
	return func(c *config) { c.timeout = d }
}

// WithMaxOutput 设置 stdout/stderr 各自的上限；n <= 0 表示用缺省值。
// 超出部分被截断（调用方拿到的切片长度即上限）。
func WithMaxOutput(n int) Option {
	return func(c *config) { c.maxOutput = n }
}

// WithShell 经 /bin/sh -c 执行：name 与 args 以空格拼接成命令串，不做
// 转义，调用方须自行保证引号正确。仅在确实需要 shell 语义时使用。
func WithShell() Option {
	return func(c *config) { c.shell = true }
}

// Error 是子进程执行失败的结构化错误。ExitCode 为 -1 表示进程未正常退出
// （启动失败/超时/被信号杀死）；Stderr 是已捕获的 stderr（可能被输出上限截断）。
type Error struct {
	Command  string
	ExitCode int
	Stderr   string
	Err      error
}

// Error 实现 error。
func (e *Error) Error() string {
	var b strings.Builder
	b.WriteString("execx: ")
	b.WriteString(e.Command)
	if e.ExitCode >= 0 {
		fmt.Fprintf(&b, "（退出码 %d）", e.ExitCode)
	}
	if s := strings.TrimSpace(e.Stderr); s != "" {
		b.WriteString(": ")
		b.WriteString(s)
	} else if e.Err != nil {
		b.WriteString(": ")
		b.WriteString(e.Err.Error())
	}
	return b.String()
}

// Unwrap 保留底层错误链（*exec.ExitError / context 错误 / exec.ErrNotFound）。
func (e *Error) Unwrap() error { return e.Err }

// Run 执行 name（args 为 argv），返回有界的 stdout/stderr。
//
// 成功时 err == nil；非零退出、启动失败或超时返回 *Error。超时同时满足
// errors.Is(err, context.DeadlineExceeded)。
func Run(ctx context.Context, name string, args []string, opts ...Option) (stdout, stderr []byte, err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	cfg := config{maxOutput: DefaultMaxOutput}
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}
	if cfg.maxOutput <= 0 {
		cfg.maxOutput = DefaultMaxOutput
	}
	if cfg.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, cfg.timeout)
		defer cancel()
	}

	cmdName, cmdArgs := name, args
	if cfg.shell {
		cmdName = shellPath
		cmdArgs = []string{"-c", shellCommand(name, args)}
	}
	cmd := exec.CommandContext(ctx, cmdName, cmdArgs...)
	cmd.WaitDelay = waitDelay
	cmd.Dir = cfg.dir
	if cfg.env != nil {
		cmd.Env = cfg.env
	}
	outBuf := &limitedBuffer{limit: cfg.maxOutput}
	errBuf := &limitedBuffer{limit: cfg.maxOutput}
	cmd.Stdout = outBuf
	cmd.Stderr = errBuf

	runErr := cmd.Run()
	stdout, stderr = outBuf.Bytes(), errBuf.Bytes()
	if runErr == nil {
		return stdout, stderr, nil
	}
	// 取消/超时优先于退出码归因：CommandContext 杀死进程后底层错误是
	// "signal: killed"，对调用方没有可操作信息。
	if cerr := ctx.Err(); cerr != nil {
		return stdout, stderr, &Error{Command: name, ExitCode: -1, Stderr: string(stderr), Err: cerr}
	}
	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) {
		return stdout, stderr, &Error{
			Command: name, ExitCode: exitErr.ExitCode(), Stderr: string(stderr), Err: runErr,
		}
	}
	// WaitDelay：leader 已退出（成功或非零）但管道的孙进程写端未关，os/exec
	// 到点强关管道并报 ErrWaitDelay。退出码仍取 ProcessState 的真实值（子进程
	// 成功退出即 0），不得误报 -1——那会把「命令已成功、只是后台孙进程持有
	// 管道」归类为启动/托管失败。
	if errors.Is(runErr, exec.ErrWaitDelay) {
		code := -1
		if cmd.ProcessState != nil {
			code = cmd.ProcessState.ExitCode()
		}
		return stdout, stderr, &Error{Command: name, ExitCode: code, Stderr: string(stderr), Err: runErr}
	}
	if errors.Is(runErr, exec.ErrNotFound) {
		runErr = fmt.Errorf("可执行文件 %q 不在 PATH 中（检查 PATH 或安装该命令）: %w", name, runErr)
	}
	return stdout, stderr, &Error{Command: name, ExitCode: -1, Stderr: string(stderr), Err: runErr}
}

// LookPath 包装 exec.LookPath：只做 PATH 解析的调用方（启动期探测）用它，
// 不要为此直接 import os/exec。未命中错误带 PATH 线索。
func LookPath(name string) (string, error) {
	path, err := exec.LookPath(name)
	if err != nil {
		return "", fmt.Errorf("execx: 可执行文件 %q 不在 PATH 中: %w", name, err)
	}
	return path, nil
}

// shellCommand 拼接 shell 命令串；不转义，引号由调用方负责。
func shellCommand(name string, args []string) string {
	if len(args) == 0 {
		return name
	}
	return name + " " + strings.Join(args, " ")
}

// limitedBuffer 是带上限的 bytes.Buffer：超过 limit 的写入被丢弃，但始终
// 报告完整写入长度——cmd 的复制协程据此继续排空管道，不会阻塞子进程。
type limitedBuffer struct {
	buf   bytes.Buffer
	limit int
}

func (w *limitedBuffer) Write(p []byte) (int, error) {
	if remaining := w.limit - w.buf.Len(); remaining > 0 {
		if len(p) > remaining {
			_, _ = w.buf.Write(p[:remaining])
		} else {
			_, _ = w.buf.Write(p)
		}
	}
	return len(p), nil
}

func (w *limitedBuffer) Bytes() []byte { return w.buf.Bytes() }
