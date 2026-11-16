// service_repomap_procdiscipline_test.go —— 执行面收口回归（issue #46）：
// repomap 克隆/探测面已迁 procx 进程组纪律（Setpgid 建组 → 超时 TERM 整组 →
// WaitDelay/宽限 SIGKILL），本文件用**真进程**钉死两条验收语义：
//
//  1. 超时 + 孙进程持 stdout 管道：Run 必须按 deadline 收口返回（ErrTimeout），
//     绝不阻塞到孙进程自然死亡——裸 exec.CommandContext.Wait 在该形态下会
//     挂满孙进程生命周期（git clone 的 git-remote-* helper 即此形态）；
//  2. leader 成功退出 + 守护化孙进程持管道：WaitDelay 强关管道产生的
//     ErrWaitDelay 不判失败，执行仍按成功收割（stdout 不丢）。
//
// 两用例均 unix-only（sh + Setpgid 语义）；总耗时预算见各断言上界。
package service

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/oagentkit/runtime/procx"
)

// TestExecTimeoutGrandchildHoldingPipe 场景 1：后台孙进程握 stdout 写端 +
// leader（exec sleep）睡满全程。Timeout=500ms；组纪律 TERM 整组后管道写端
// 全关、Wait 立即解除——断言总耗时远小于孙进程寿命（60s），错误语义为
// ErrTimeout。无纪律参照：裸 Wait 会阻塞到 ~60s。
func TestExecTimeoutGrandchildHoldingPipe(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix-only：sh + Setpgid 进程组语义")
	}
	start := time.Now()
	_, _, _, err := procx.Run(context.Background(), procx.RunRequest{
		Argv:    []string{"sh", "-c", "sleep 60 & echo started; exec sleep 60"},
		Timeout: 500 * time.Millisecond,
	})
	elapsed := time.Since(start)
	if !errors.Is(err, procx.ErrTimeout) {
		t.Fatalf("应判 ErrTimeout: %v", err)
	}
	if elapsed >= 10*time.Second {
		t.Fatalf("超时收口挂死：耗时 %v（应远小于孙进程寿命 60s——孙进程持管道未被组纪律收口）", elapsed)
	}
}

// TestExecWaitDelayBenignGrandchildPipe 场景 2：leader 立即成功退出、守护化
// 孙进程（sleep 30，未重定向）继续握 stdout 写端。WaitDelay 到点强关管道，
// ErrWaitDelay 按良性特判 → 执行成功、exit 0、leader 的 stdout 完整。
// 断言总耗时远小于孙进程寿命（无纪律参照：Wait 阻塞 ~30s）。
func TestExecWaitDelayBenignGrandchildPipe(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix-only：sh + Setpgid 进程组语义")
	}
	start := time.Now()
	stdout, _, code, err := procx.Run(context.Background(), procx.RunRequest{
		Argv:    []string{"sh", "-c", "sleep 30 & echo leader-done; exit 0"},
		Timeout: 20 * time.Second,
	})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("ErrWaitDelay 应良性特判为成功: %v", err)
	}
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if !strings.Contains(stdout, "leader-done") {
		t.Fatalf("stdout 应含 leader 输出, got %q", stdout)
	}
	if elapsed >= 20*time.Second {
		t.Fatalf("良性 WaitDelay 收割挂死：耗时 %v（应远小于孙进程寿命 30s）", elapsed)
	}
}
