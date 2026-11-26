// Package fixcheck 修复补丁的内置语法验证器（供 ARGUS_FIX_VERIFY_CMD 使用）：
//
//	ARGUS_FIX_VERIFY_CMD=/app/argus fixcheck {dir}
//
// 语义：只对工作副本里「本轮被补丁改动」的文件做**语法级**校验，不执行仓库
// 代码、不写文件（Python 用 ast.parse，不落 __pycache__）。覆盖：
//
//	.go  go/parser 全错误集
//	.json json.Valid
//	.yaml/.yml yaml.v3 解析
//	.sh/.bash sh -n（busybox/POSIX 语法）
//	.py  python3 + ast.parse（镜像无 python3 则跳过）
//	.js/.mjs/.cjs node --check（镜像无 node 则跳过）
//
// fail-closed 纪律：**没有任何可检查文件 → 退出 1**（不能把「无法验证」当成
// 「验证通过」，否则未验证的修复会被打上 ✅ 并进入推送候选）。任一文件语法
// 失败 → 退出 1；全部通过 → 退出 0。
package fixcheck

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Run 是 cli 子命令入口：argus fixcheck <worktree-dir>。
func Run(args []string) int {
	fs := flag.NewFlagSet("fixcheck", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	verbose := fs.Bool("v", false, "打印每个文件的检查结果")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "用法: argus fixcheck [-v] <worktree-dir>")
		return 2
	}
	dir := fs.Arg(0)
	res, err := CheckDir(context.Background(), dir, *verbose)
	if err != nil {
		fmt.Fprintf(os.Stderr, "fixcheck: %v\n", err)
		return 2
	}
	for _, line := range res.Lines {
		fmt.Println(line)
	}
	if res.Checked == 0 {
		fmt.Printf("fixcheck: 无可验证文件（改动 %d 个，跳过 %d 个）——fail-closed 不认为验证通过\n", res.Changed, res.Skipped)
		return 1
	}
	if res.Failed > 0 {
		fmt.Printf("fixcheck: %d/%d 个文件语法失败\n", res.Failed, res.Checked)
		return 1
	}
	fmt.Printf("fixcheck: %d 个文件语法通过（跳过 %d 个不可检查文件）\n", res.Checked, res.Skipped)
	return 0
}

// Result 一次检查的汇总。
type Result struct {
	Changed int      // git status 中的改动文件数
	Checked int      // 实际执行语法检查的文件数
	Skipped int      // 无可用检查器的文件数
	Failed  int      // 语法失败数
	Lines   []string // 明细（-v 时为每个文件一行，失败始终记录）
}

// CheckDir 检查 dir 工作副本中本轮改动的文件。
func CheckDir(ctx context.Context, dir string, verbose bool) (*Result, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("目录解析失败: %w", err)
	}
	if st, err := os.Stat(abs); err != nil || !st.IsDir() {
		return nil, fmt.Errorf("工作副本不可用: %s", abs)
	}
	files, err := changedFiles(ctx, abs)
	if err != nil {
		return nil, err
	}
	res := &Result{Changed: len(files)}
	for _, rel := range files {
		path := filepath.Join(abs, rel)
		checker, ok := checkerFor(path)
		if !ok {
			res.Skipped++
			continue
		}
		if err := checker(ctx, path); err != nil {
			var skip *skipError
			if errors.As(err, &skip) {
				res.Skipped++
				if verbose {
					res.Lines = append(res.Lines, fmt.Sprintf("skip %s: %s", rel, skip.reason))
				}
				continue
			}
			res.Failed++
			res.Lines = append(res.Lines, fmt.Sprintf("FAIL %s: %v", rel, err))
			continue
		}
		res.Checked++
		if verbose {
			res.Lines = append(res.Lines, fmt.Sprintf("ok   %s", rel))
		}
	}
	return res, nil
}

// changedFiles 返回 git status 中的改动文件（含未跟踪；重命名取新路径）。
// -z 输出：`XY <path>\0`；重命名/复制记录额外带旧路径一段。
func changedFiles(ctx context.Context, dir string) ([]string, error) {
	out, err := runGit(ctx, dir, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return nil, err
	}
	var files []string
	records := strings.Split(out, "\x00")
	for i := 0; i < len(records); i++ {
		rec := records[i]
		if len(rec) < 4 {
			continue
		}
		status := rec[:2]
		path := strings.TrimSpace(rec[3:])
		if path == "" {
			continue
		}
		files = append(files, path)
		// 重命名/复制：-z 下旧路径是紧随其后的独立记录。
		if status[0] == 'R' || status[0] == 'C' || status[1] == 'R' || status[1] == 'C' {
			i++
		}
	}
	return files, nil
}

func runGit(ctx context.Context, dir string, args ...string) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, "git", append([]string{"-C", dir}, args...)...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s 失败: %w (%s)", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

// checkerFor 按扩展名选择检查器；ok=false 表示无可用检查器（计入 skipped）。
func checkerFor(path string) (func(context.Context, string) error, bool) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".go":
		return checkGo, true
	case ".json":
		return checkJSON, true
	case ".yaml", ".yml":
		return checkYAML, true
	case ".sh", ".bash":
		return checkShell, true
	case ".py":
		return checkPython, true
	case ".js", ".mjs", ".cjs":
		return checkNode, true
	default:
		return nil, false
	}
}

func checkGo(_ context.Context, path string) error {
	fset := token.NewFileSet()
	_, err := parser.ParseFile(fset, path, nil, parser.AllErrors|parser.ParseComments)
	return err
}

func checkJSON(_ context.Context, path string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if !json.Valid(raw) {
		return errors.New("JSON 语法无效")
	}
	return nil
}

func checkYAML(_ context.Context, path string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var v any
	return yaml.Unmarshal(raw, &v)
}

func checkShell(ctx context.Context, path string) error {
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, "sh", "-n", path)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("sh -n: %v (%s)", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

func checkPython(ctx context.Context, path string) error {
	if _, err := exec.LookPath("python3"); err != nil {
		return errSkip("python3 不可用")
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	// ast.parse 而非 py_compile：不产生 __pycache__ 副作用。
	cmd := exec.CommandContext(cctx, "python3", "-c",
		"import ast,sys; ast.parse(open(sys.argv[1], encoding='utf-8').read(), filename=sys.argv[1])", path)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("python ast.parse: %v (%s)", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

func checkNode(ctx context.Context, path string) error {
	if _, err := exec.LookPath("node"); err != nil {
		return errSkip("node 不可用")
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, "node", "--check", path)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("node --check: %v (%s)", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// errSkip 运行时缺少解释器：不应算语法失败，也不应算通过——计入 skipped。
// CheckDir 通过 errors.As 识别并跳过。
type skipError struct{ reason string }

func (e *skipError) Error() string { return e.reason }

func errSkip(reason string) error { return &skipError{reason: reason} }
