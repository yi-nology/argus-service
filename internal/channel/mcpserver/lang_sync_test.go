package mcpserver

import (
	"strings"
	"testing"

	"github.com/oagentkit/textx/codeintel"
)

// TestLangDescSync 语言口径防漂移：get_impact/get_repo_map 的描述须覆盖 langcap
// 对应能力面的全部语言展示名——mcpserver 生产面零 internal 依赖，描述词表与
// langcap 单源的一致性在此钉（描述扩语言而此处漏 = 口径重回散落）；改描述须
// 同步 skills/argus-mcp/references/tools.md。
func TestLangDescSync(t *testing.T) {
	cases := []struct {
		tool string
		desc string
		want []string
	}{
		{"get_impact", impactTool().Description,
			append([]string{"Go"}, codeintel.LangsOf(codeintel.FuncDiffExts)...)},
		{"get_repo_map", repoMapTool().Description,
			append([]string{"Go"}, codeintel.LangsOf(codeintel.ScanExts)...)},
	}
	for _, c := range cases {
		for _, name := range c.want {
			if !strings.Contains(c.desc, name) {
				t.Fatalf("%s 描述缺语言 %q（langcap 口径）：%s", c.tool, name, c.desc)
			}
		}
	}
}
