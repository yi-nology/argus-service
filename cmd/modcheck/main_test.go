package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// modJSONFromContent 把 go.mod 文本交给 `go mod edit -json` 解析成门禁输入。
// 用真实命令而非手拼 JSON：判据消费的就是生产路径的同一 AST 形态。
func modJSONFromContent(t *testing.T, content string) []byte {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "mod", "edit", "-json")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go mod edit -json: %v", err)
	}
	return out
}

// 两条登记项（块形态）放行。
func TestCheckAllowsRegisteredReplaces(t *testing.T) {
	json := modJSONFromContent(t, `module example.com/m

go 1.22

replace (
	github.com/apache/thrift => github.com/apache/thrift v0.13.0
	github.com/glebarez/go-sqlite => ./third_party/gosqliteshim
)
`)
	if bad := check(json, nil, nil); len(bad) != 0 {
		t.Fatalf("登记项应放行，实得 %v", bad)
	}
}

// 前导空格的 ` replace` 形态必须被看见（文本 grep 易漏的绕过写法）。
func TestCheckCatchesLeadingSpaceReplace(t *testing.T) {
	json := modJSONFromContent(t, `module example.com/m

go 1.22

 replace github.com/evil/a => github.com/evil/a v1.0.0
`)
	bad := check(json, nil, nil)
	if len(bad) != 1 {
		t.Fatalf("应恰有一条违例，实得 %v", bad)
	}
	if !strings.Contains(bad[0], "github.com/evil/a") || !strings.Contains(bad[0], "未登记的 replace") {
		t.Fatalf("违例内容不符: %q", bad[0])
	}
}

// `replace (...)` 块形态下的未登记项同样命中，且已登记项不误报。
func TestCheckCatchesBlockReplace(t *testing.T) {
	json := modJSONFromContent(t, `module example.com/m

go 1.22

replace (
	github.com/apache/thrift => github.com/apache/thrift v0.13.0
	github.com/glebarez/go-sqlite => ./third_party/gosqliteshim
	github.com/evil/b => github.com/evil/b v2.0.0
)
`)
	bad := check(json, nil, nil)
	if len(bad) != 1 || !strings.Contains(bad[0], "github.com/evil/b") {
		t.Fatalf("块形态应只命中未登记项，实得 %v", bad)
	}
}

// 同一 replace 目标但版本不符同样拒绝（允许清单是三元组全等，不是前缀）。
func TestCheckRejectsWrongReplacedVersion(t *testing.T) {
	json := modJSONFromContent(t, `module example.com/m

go 1.22

replace github.com/apache/thrift => github.com/apache/thrift v0.14.0
`)
	bad := check(json, nil, nil)
	if len(bad) != 1 || !strings.Contains(bad[0], "v0.14.0") {
		t.Fatalf("版本漂移应拒绝，实得 %v", bad)
	}
}

// go.work 存在即红（module 解析必须单源），验证失败也一并汇总。
func TestCheckBlocksGoWorkAndVerifyFailure(t *testing.T) {
	bad := check([]byte(`{"Replace":null}`), []string{"go.work", "sub/go.work"},
		errors.New("checksum mismatch"))
	if len(bad) != 2 {
		t.Fatalf("应同时报 go.work 与 verify 失败，实得 %v", bad)
	}
	if !strings.Contains(bad[0], "go.work") || !strings.Contains(bad[0], "sub/go.work") {
		t.Fatalf("go.work 违例应列出全部命中: %q", bad[0])
	}
	if !strings.Contains(bad[1], "go mod verify 失败: checksum mismatch") {
		t.Fatalf("verify 违例内容不符: %q", bad[1])
	}
}

// 非法 JSON 是门禁故障，必须红而不是当成「无 replace」放行。
func TestCheckRejectsInvalidJSON(t *testing.T) {
	bad := check([]byte(`not-json`), nil, nil)
	if len(bad) != 1 || !strings.Contains(bad[0], "解析 go mod edit -json 失败") {
		t.Fatalf("非法 JSON 应报错，实得 %v", bad)
	}
}

// go.work 清单筛选：只留 basename 为 go.work 的条目（含未跟踪/子目录形态）。
func TestGoWorkFiles(t *testing.T) {
	listing := strings.Join([]string{
		"main.go",
		"go.mod",
		"go.work",
		"sub/go.work",
		"go.work.sum",
		"docs/go.work.bak",
		"",
	}, "\n")
	got := goWorkFiles(listing)
	want := []string{"go.work", "sub/go.work"}
	if len(got) != len(want) {
		t.Fatalf("goWorkFiles = %v，期望 %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("goWorkFiles = %v，期望 %v", got, want)
		}
	}
}
