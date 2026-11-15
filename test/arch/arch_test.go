// Package arch 保留 archcheck 覆盖不到的架构守卫：包文档完整性。
//
// 分层依赖方向、未登记包、组合根单向性、裸 goroutine、os/exec 单一出口等
// 已由 `architecture-policy.yaml` + `cmd/archcheck`（`make arch`）单源执行；
// 本文件不再复制 layering 白名单——两份真相必然漂移（v7.3.x 重构批）。
package arch

import (
	"os/exec"
	"strings"
	"testing"
)

// docAllowlist 包文档检查的暂缓名单（原因必填）。当前为空。
var docAllowlist = map[string]string{}

// TestPackageDocs 包文档完整性：包级 doc comment 是架构可读性的第一道门——
// 职责、层级、边界写在 go doc 第一屏。archcheck 没有这项判据，故留在这里。
func TestPackageDocs(t *testing.T) {
	out, err := exec.Command("go", "list", "-f", `{{.ImportPath}}|{{.Doc}}`,
		"github.com/yi-nology/argus/internal/...").Output()
	if err != nil {
		t.Fatalf("go list 失败: %v", err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		parts := strings.SplitN(line, "|", 2)
		if len(parts) != 2 {
			continue
		}
		pkg, doc := parts[0], parts[1]
		if doc == "" {
			if reason, ok := docAllowlist[pkg]; ok {
				t.Logf("包 %s 缺 doc（暂缓：%s）", pkg, reason)
				continue
			}
			t.Errorf("包 %s 缺包级 doc comment：请在包内任一文件 package 子句上方补「// Package <name> 职责一句话（层级/边界要点）」", pkg)
		}
	}
}
