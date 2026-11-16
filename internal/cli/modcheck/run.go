// Package modcheck 模块面门禁：replace 允许清单 + 禁仓内 go.work + go mod verify。
//
// 判据只认 `go mod edit -json` 的 AST，不做文本 grep：` replace`（前导空格）、
// `replace (` 块、跨行形态在文本层各有写法，在 JSON 里都是同一条 Replace 记录。
// 「换一种写法就能绕过门禁」的缺口必须在判据层面关闭，而不是把正则写得更长。
//
// 仅允许两条已登记替换：
//   - github.com/apache/thrift => github.com/apache/thrift v0.13.0（钉版本）
//   - github.com/glebarez/go-sqlite => ./third_party/gosqliteshim（纯 Go 垫片）
//
// 新增/变更任何 replace 都是显式动作：先登记本清单并说明理由，否则命令红。
package modcheck

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path"
	"sort"
	"strings"

	"github.com/yi-nology/argus/internal/execx"
)

// replaceRule 允许的 replace 条目（旧/新 path、旧/新 version 全等匹配）。
// OldVersion/Version 为空表示该侧不带版本（目录替换或未限定旧版本）。
type replaceRule struct {
	OldPath    string
	OldVersion string
	NewPath    string
	Version    string
}

// allowedReplaces 是仓库仅有的两条登记项（顺序无关，匹配以条目全等为准）。
var allowedReplaces = []replaceRule{
	{OldPath: "github.com/apache/thrift", NewPath: "github.com/apache/thrift", Version: "v0.13.0"},
	{OldPath: "github.com/glebarez/go-sqlite", NewPath: "./third_party/gosqliteshim"},
}

// moduleRef 是 go.mod 中模块引用（path + 可选 version）。
type moduleRef struct {
	Path    string
	Version string
}

// goModReplace 是 goModEditJSON.Replace 的单项。
type goModReplace struct {
	Old moduleRef
	New moduleRef
}

// goModEditJSON 是 `go mod edit -json` 输出的最小投影（只取判据所需字段；
// encoding/json 对字段名大小写不敏感，Go 侧命名保持可读）。
type goModEditJSON struct {
	Replace []goModReplace
}

// parseGoModEditJSON 解析 `go mod edit -json` 输出。解析失败是门禁自身故障，
// 必须红——静默把「解析不了」当成「没有 replace」等于给 bypass 留门。
func parseGoModEditJSON(data []byte) ([]goModReplace, error) {
	var doc goModEditJSON
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("解析 go mod edit -json 失败: %w", err)
	}
	return doc.Replace, nil
}

// checkReplaces 逐条比对允许清单，返回未登记的 replace（空 = 通过）。
// 四元组全等：旧 path/version、新 path/version——只钉旧 path 会漏掉
// 「旧版本漂移」（如 thrift v0.12.0 => v0.13.0），那同样是一次未登记变更。
// 这里只做允许面判定：清单条目被上游修复后移除不是违规（不强制两条都在）。
func checkReplaces(replaces []goModReplace) []string {
	var bad []string
	for _, r := range replaces {
		allowed := false
		for _, want := range allowedReplaces {
			if r.Old.Path == want.OldPath && r.Old.Version == want.OldVersion &&
				r.New.Path == want.NewPath && r.New.Version == want.Version {
				allowed = true
				break
			}
		}
		if !allowed {
			bad = append(bad, fmt.Sprintf("未登记的 replace：%s => %s（新增替换须先登记并说明理由）",
				formatRef(r.Old), formatRef(r.New)))
		}
	}
	return bad
}

func formatRef(r moduleRef) string {
	if r.Version == "" {
		return r.Path
	}
	return r.Path + " " + r.Version
}

// goWorkFiles 从 `git ls-files -co` 输出中筛出 go.work（含未跟踪文件；
// 子目录同名文件同样命中）。gate 要求仓内 module 解析单源在 go.mod。
func goWorkFiles(listing string) []string {
	var out []string
	for _, line := range strings.Split(listing, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if path.Base(line) == "go.work" {
			out = append(out, line)
		}
	}
	sort.Strings(out)
	return out
}

// check 是门禁的纯判据：modJSON 来自 `go mod edit -json`，workFiles 是仓内
// go.work 清单，verifyErr 是 `go mod verify` 的结果。返回全部违规项。
func check(modJSON []byte, workFiles []string, verifyErr error) []string {
	var bad []string
	if len(workFiles) > 0 {
		bad = append(bad, "禁止仓内 go.work（module 解析必须单源在 go.mod）："+
			strings.Join(workFiles, ", "))
	}
	replaces, err := parseGoModEditJSON(modJSON)
	if err != nil {
		bad = append(bad, err.Error())
	} else {
		bad = append(bad, checkReplaces(replaces)...)
	}
	if verifyErr != nil {
		bad = append(bad, "go mod verify 失败: "+verifyErr.Error())
	}
	return bad
}

// goModEditJSONFrom 在 dir 下执行 `go mod edit -json`。
func goModEditJSONFrom(dir string) ([]byte, error) {
	out, stderr, err := execx.Run(context.Background(), "go", []string{"mod", "edit", "-json"}, execx.WithDir(dir))
	if err != nil {
		return nil, fmt.Errorf("go mod edit -json 失败: %s", strings.TrimSpace(string(stderr)))
	}
	return out, nil
}

// gitListFiles 返回 dir 下已跟踪 + 未忽略的未跟踪文件（`git ls-files -co`）。
func gitListFiles(dir string) (string, error) {
	out, stderr, err := execx.Run(context.Background(), "git", []string{"ls-files", "-co"}, execx.WithDir(dir))
	if err != nil {
		return "", fmt.Errorf("git ls-files 失败: %s", strings.TrimSpace(string(stderr)))
	}
	return string(out), nil
}

// runGoModVerify 执行 `go mod verify`，失败时带上命令输出便于排障。
func runGoModVerify(dir string) error {
	stdout, stderr, err := execx.Run(context.Background(), "go", []string{"mod", "verify"}, execx.WithDir(dir))
	if err != nil {
		out := strings.TrimSpace(string(stdout) + "\n" + string(stderr))
		return fmt.Errorf("%w: %s", err, out)
	}
	return nil
}

// run 汇总三项判据。
func run(dir string) error {
	modJSON, err := goModEditJSONFrom(dir)
	if err != nil {
		return err
	}
	listing, err := gitListFiles(dir)
	if err != nil {
		return err
	}
	bad := check(modJSON, goWorkFiles(listing), runGoModVerify(dir))
	if len(bad) == 0 {
		return nil
	}
	return fmt.Errorf("模块面门禁未通过：\n- %s", strings.Join(bad, "\n- "))
}

func Run(args []string) int {
	fs := flag.NewFlagSet("argus modcheck", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if err := run("."); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}
