// Package archgen 从当前 go list 事实生成 architecture-policy.yaml 的 modules 段。
//
// 只在初始化/大规模增删包时使用；输出到 stdout，不覆盖 policy 文件——
// rules/thresholds/exceptions 是人工声明，重跑生成器绝不能冲掉它们。
// 生成后人工补 notes/分层说明，并跑 `make arch` 验证。
package archgen

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/yi-nology/argus/internal/execx"
)

func moduleID(importPath string) string {
	base := importPath[strings.LastIndex(importPath, "/")+1:]
	if base == "requirement" {
		return "requirement"
	}
	return base
}

func extRoot(importPath string) string {
	parts := strings.Split(importPath, "/")
	switch {
	case strings.HasPrefix(importPath, "github.com/") && len(parts) >= 3:
		return strings.Join(parts[:3], "/")
	case strings.HasPrefix(importPath, "gorm.io/") && len(parts) >= 2:
		return strings.Join(parts[:2], "/")
	}
	return importPath
}

func dedup(in []string) []string {
	out := in[:0]
	for i, s := range in {
		if i == 0 || s != in[i-1] {
			out = append(out, s)
		}
	}
	return out
}

func Run(args []string) int {
	fs := flag.NewFlagSet("argus archgen", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	out, stderr, err := execx.Run(context.Background(), "go",
		[]string{"list", "-f", `{{.ImportPath}}|{{join .Imports ","}}`, "./internal/..."})
	if err != nil {
		fmt.Fprintf(os.Stderr, "archgen: %v\n%s", err, stderr)
		return 2
	}
	var b strings.Builder
	b.WriteString("modules:\n")
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		parts := strings.SplitN(line, "|", 2)
		id := moduleID(parts[0])
		var reqs []string
		if len(parts) == 2 && parts[1] != "" {
			for _, imp := range strings.Split(parts[1], ",") {
				switch {
				case strings.HasPrefix(imp, "github.com/yi-nology/argus/internal/"):
					reqs = append(reqs, moduleID(imp))
				case strings.HasPrefix(imp, "github.com/yi-nology/argus/"):
					// biz/router 等生成面：不登记为 module
				case strings.Contains(imp, "."):
					reqs = append(reqs, "ext:"+extRoot(imp))
				}
			}
		}
		sort.Strings(reqs)
		reqs = dedup(reqs)
		fmt.Fprintf(&b, "  - id: %s\n    roots: [%s]\n    requires: [%s]\n    owner: argus\n    managed: true\n",
			id, strings.TrimPrefix(parts[0], "github.com/yi-nology/argus/"), strings.Join(reqs, ", "))
	}
	fmt.Print(b.String())
	return 0
}
