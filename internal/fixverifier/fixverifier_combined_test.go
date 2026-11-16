// fixverifier_combined_test.go —— VerifyCombined（#61 组合补丁最终验证）单测：
// Apply 全部通过项后对组合树整体跑一次包装命令，单条各自通过 ≠ 组合可构建。
package fixverifier

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeCombinedTree(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("fixed-one\nfixed-two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// 组合树两处修复都在 → 包装命令通过。
func TestVerifyCombinedOK(t *testing.T) {
	dir := writeCombinedTree(t)
	ok, out := VerifyCombined(context.Background(), Config{
		Cmd: "grep -q fixed-one {dir}/a.go && grep -q fixed-two {dir}/a.go",
	}, dir)
	if !ok {
		t.Fatalf("组合树应通过, output=%q", out)
	}
}

// 组合失败：输出尾部保留可诊断（ boom 标记必须出现在 output）。
func TestVerifyCombinedFailKeepsTail(t *testing.T) {
	dir := writeCombinedTree(t)
	ok, out := VerifyCombined(context.Background(), Config{
		Cmd: "echo boom-combined >&2; exit 1",
	}, dir)
	if ok {
		t.Fatal("组合树校验失败应 ok=false")
	}
	if !strings.Contains(out, "boom-combined") {
		t.Fatalf("output 应保留失败尾部诊断: %q", out)
	}
}

// Cmd 未配置 = 无验证能力，不得阻断（与 Verify 停用语义一致）。
func TestVerifyCombinedNoCmdPasses(t *testing.T) {
	ok, out := VerifyCombined(context.Background(), Config{}, t.TempDir())
	if !ok || out != "" {
		t.Fatalf("无包装命令应放行: ok=%v out=%q", ok, out)
	}
}

// 超时传导：包装命令挂死 → 按超时失败（ok=false），不是永久阻塞。
func TestVerifyCombinedTimeout(t *testing.T) {
	if ok, _ := VerifyCombined(context.Background(), Config{
		Cmd: "sleep 5", Timeout: 200 * time.Millisecond,
	}, t.TempDir()); ok {
		t.Fatal("超时应视为组合验证失败")
	}
}
