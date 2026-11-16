package runner

// v7.5 #69 源码证据读取面：预算（单文件字节/行数/超时）+ 按 path 缓存
// （同名文件只读一次）回归。真 workcopy 池 + 本地 file:// fixture 仓，
// 不用 mock 绕开 Ensure/Release 的真实借还路径。

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/oagentkit/tools/workcopy"
	"github.com/oekit/logx"

	"github.com/yi-nology/argus/internal/plugin"
)

// makeSourceFixture 造一个带 refs/pull/7/head 的本地仓；返回目录与 head SHA。
func makeSourceFixture(t *testing.T, files map[string]string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=t@t",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run("init", "-b", "main")
	for name, body := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run("add", ".")
	run("commit", "-m", "init")
	sha := run("rev-parse", "HEAD")
	run("update-ref", "refs/pull/7/head", "main")
	return dir, sha
}

func newSourceReaderFor(t *testing.T, fixture, sha string) *workcopySourceReader {
	t.Helper()
	wc := workcopy.NewPool(t.TempDir(), logx.Nop())
	wc.CredentialOf = func(string) (string, string, bool) { return "file://" + fixture, "x", true }
	return newWorkcopySourceReader(context.Background(), wc, workcopy.WorktreeKey{
		Platform: "gitea", Owner: "acme", Repo: "app", Number: "7",
		HeadSHA: sha, DefaultBranch: "main",
	})
}

// TestSourceReaderCachesFile 同一文件多次引用只真正读一次（#69 验收钉）。
func TestSourceReaderCachesFile(t *testing.T) {
	fixture, sha := makeSourceFixture(t, map[string]string{
		"src/a.go": "package a\n\nfunc A() {}\n",
	})
	r := newSourceReaderFor(t, fixture, sha)

	first, err := r.ReadLines("src/a.go")
	if err != nil {
		t.Fatalf("ReadLines: %v", err)
	}
	second, err := r.ReadLines("src/a.go")
	if err != nil {
		t.Fatalf("ReadLines(2): %v", err)
	}
	if len(first) != 4 || len(second) != 4 || second[2] != "func A() {}" {
		t.Fatalf("读取内容失真: %v / %v", first, second)
	}
	if r.reads != 1 {
		t.Fatalf("同文件应只读一次, reads=%d", r.reads)
	}

	// 不存在的文件也缓存：错误不重复读（cache 命中不触发 Ensure/Open）。
	if _, err := r.ReadLines("src/missing.go"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("缺失文件应透传 fs.ErrNotExist: %v", err)
	}
	if _, err := r.ReadLines("src/missing.go"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("缺失文件二次读取错误应稳定: %v", err)
	}
	if _, ok := r.cache["src/missing.go"]; !ok {
		t.Fatal("缺失结果未进缓存")
	}
}

// TestSourceReaderFileTooLarge 单文件字节预算：超限返回可分类错误（核验层
// 降级 unverified 而非 invalid），且超限结果也走缓存只读一次。
func TestSourceReaderFileTooLarge(t *testing.T) {
	fixture, sha := makeSourceFixture(t, map[string]string{
		"big.txt": strings.Repeat("0123456789", 20), // 200 字节
	})
	r := newSourceReaderFor(t, fixture, sha)
	r.maxFileBytes = 32

	_, err := r.ReadLines("big.txt")
	if !errors.Is(err, plugin.ErrSourceFileTooLarge) {
		t.Fatalf("超字节预算应 ErrSourceFileTooLarge: %v", err)
	}
	if errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("预算超限不得冒充分文件不存在: %v", err)
	}
	if _, err := r.ReadLines("big.txt"); !errors.Is(err, plugin.ErrSourceFileTooLarge) {
		t.Fatalf("超限结果应缓存: %v", err)
	}
	if r.reads != 1 {
		t.Fatalf("超限文件只应读一次, reads=%d", r.reads)
	}
}

// TestSourceReaderLineCap 单文件行数预算：超限同样可分类降级。
func TestSourceReaderLineCap(t *testing.T) {
	fixture, sha := makeSourceFixture(t, map[string]string{
		"many.txt": "l1\nl2\nl3\nl4\nl5\n",
	})
	r := newSourceReaderFor(t, fixture, sha)
	r.maxFileLines = 3

	_, err := r.ReadLines("many.txt")
	if !errors.Is(err, plugin.ErrSourceFileTooLarge) {
		t.Fatalf("超行数预算应 ErrSourceFileTooLarge: %v", err)
	}
}

// TestSourceReaderTimeout ctx 贯穿：已取消的 ctx 不应被当成文件不存在。
func TestSourceReaderTimeout(t *testing.T) {
	fixture, sha := makeSourceFixture(t, map[string]string{"a.go": "package a\n"})
	r := newSourceReaderFor(t, fixture, sha)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r.ctx = ctx

	_, err := r.ReadLines("a.go")
	if err == nil {
		t.Fatal("已取消的 ctx 应返回错误")
	}
	if errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("超时/取消不得冒充分文件不存在: %v", err)
	}
}

// TestSourceReaderPathFence 围栏：越界路径（../../etc/passwd）必须被拒绝，
// 且不会被缓存成可读内容。
func TestSourceReaderPathFence(t *testing.T) {
	fixture, sha := makeSourceFixture(t, map[string]string{"a.go": "package a\n"})
	r := newSourceReaderFor(t, fixture, sha)

	if _, err := r.ReadLines("../../../../etc/passwd"); err == nil {
		t.Fatal("越界路径应被围栏拒绝")
	}
}
