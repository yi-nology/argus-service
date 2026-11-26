package fixcheck

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func gitInit(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	cmd := exec.Command("git", "init", "-q")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	return dir
}

func write(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCheckDirGoJSONYAMLShell(t *testing.T) {
	dir := gitInit(t)
	write(t, dir, "ok.go", "package demo\n\nfunc F() int { return 1 }\n")
	write(t, dir, "ok.json", `{"a":1}`)
	write(t, dir, "ok.yaml", "a: 1\nb: [x, y]\n")
	write(t, dir, "ok.sh", "#!/bin/sh\nset -e\necho ok\n")

	res, err := CheckDir(context.Background(), dir, true)
	if err != nil {
		t.Fatal(err)
	}
	if res.Failed != 0 || res.Checked != 4 {
		t.Fatalf("期望 4 个通过: %+v", res)
	}
}

func TestCheckDirReportsSyntaxErrors(t *testing.T) {
	dir := gitInit(t)
	write(t, dir, "bad.go", "package demo\nfunc F( {\n")
	write(t, dir, "bad.json", `{"a":}`)
	write(t, dir, "bad.yaml", "a: [\n")
	write(t, dir, "bad.sh", "if then\n")

	res, err := CheckDir(context.Background(), dir, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Failed != 4 {
		t.Fatalf("期望 4 个失败: %+v lines=%v", res, res.Lines)
	}
}

func TestCheckDirFailClosedWhenNothingCheckable(t *testing.T) {
	dir := gitInit(t)
	write(t, dir, "README.md", "# hi\n")
	write(t, dir, "App.vue", "<template/>\n")

	res, err := CheckDir(context.Background(), dir, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Checked != 0 || res.Skipped != 2 || res.Failed != 0 {
		t.Fatalf("期望 0 检查/2 跳过: %+v", res)
	}
}

func TestCheckDirNoChanges(t *testing.T) {
	dir := gitInit(t)
	res, err := CheckDir(context.Background(), dir, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Changed != 0 || res.Checked != 0 {
		t.Fatalf("空仓应零改动: %+v", res)
	}
}

func TestCheckPython(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 不可用")
	}
	dir := gitInit(t)
	write(t, dir, "ok.py", "def f():\n    return 1\n")
	write(t, dir, "bad.py", "def f(:\n")

	res, err := CheckDir(context.Background(), dir, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Checked != 1 || res.Failed != 1 {
		t.Fatalf("期望 1 通过 1 失败: %+v %v", res, res.Lines)
	}
}

func TestCheckNode(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node 不可用")
	}
	dir := gitInit(t)
	write(t, dir, "ok.js", "const a = 1;\n")
	write(t, dir, "bad.js", "const = ;\n")
	res, err := CheckDir(context.Background(), dir, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Checked != 1 || res.Failed != 1 {
		t.Fatalf("期望 1 通过 1 失败: %+v %v", res, res.Lines)
	}
}