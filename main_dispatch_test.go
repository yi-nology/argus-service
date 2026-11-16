package main

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
)

func TestResolveDefaultServe(t *testing.T) {
	name, rest := resolve(nil)
	if name != "serve" || len(rest) != 0 {
		t.Fatalf("resolve(nil) = %q, %v; want serve, []", name, rest)
	}
	name, rest = resolve([]string{"archcheck", "-policy", "x"})
	if name != "archcheck" || len(rest) != 2 || rest[0] != "-policy" {
		t.Fatalf("resolve = %q, %v", name, rest)
	}
}

func TestRunWithHelp(t *testing.T) {
	var out, errb bytes.Buffer
	if code := runWith([]string{"help"}, "argus", &out, &errb); code != 0 {
		t.Fatalf("help code = %d, want 0", code)
	}
	if !strings.Contains(out.String(), "用法") {
		t.Fatalf("help 输出缺少用法:\n%s", out.String())
	}
}

func TestRunWithVersion(t *testing.T) {
	var out, errb bytes.Buffer
	if code := runWith([]string{"version"}, "argus", &out, &errb); code != 0 {
		t.Fatalf("version code = %d, want 0", code)
	}
	if !strings.Contains(out.String(), version) {
		t.Fatalf("version 输出 = %q, want 含 %q", out.String(), version)
	}
}

func TestRunWithUnknownCommand(t *testing.T) {
	var out, errb bytes.Buffer
	if code := runWith([]string{"nope"}, "argus", &out, &errb); code != 2 {
		t.Fatalf("unknown code = %d, want 2", code)
	}
	if !strings.Contains(errb.String(), "未知命令") {
		t.Fatalf("stderr = %q, want 未知命令", errb.String())
	}
}

func TestRunWithOCRShimArgv0(t *testing.T) {
	var out, errb bytes.Buffer
	// flag 包的帮助输出直接写 os.Stderr，测试内接管以断言路由结果。
	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	code := runWith([]string{"-h"}, "/tmp/ocrshim", &out, &errb)
	_ = w.Close()
	os.Stderr = old
	raw, _ := io.ReadAll(r)
	if code != 0 {
		t.Fatalf("argv0=ocrshim -h code = %d, want 0\n%s", code, raw)
	}
	if !strings.Contains(string(raw), "listen") {
		t.Fatalf("未路由到 ocrshim 帮助:\n%s", raw)
	}
}
