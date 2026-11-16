// exec_plane_test.go —— 执行面单源探针（issue #50，argus-exec-single-entry 仓内落点）：
// Argus 服务进程的外部命令执行**只有一条纪律面**——oagentkit/runtime/procx
// （进程组托管：unix Setpgid 建组 → 超时/取消 TERM 整组 → 宽限 SIGKILL +
// cmd.WaitDelay 管道收口，见 runtime procx 包 doc 与 issue #46 回归
// internal/service/service_repomap_procdiscipline_test.go）。
//
// 两条规则（探针 = 白名单快照，与 layering 同哲学：任何变更必须在 diff 里显式可见）：
//  1. procx 消费面封闭：import `oagentkit/runtime/procx` 的包必须在
//     execPlaneProcxAllow 白名单内——防止 procx.Run 静默扩散让「单源」声明失真；
//     新消费方 = 架构变更，先确认纪律适用（进程树/管道收口语义）再显式扩集。
//  2. 直接 os/exec 禁令：import `os/exec` 的包必须在 execPlaneDirectExecAllow
//     白名单内。当前仅剩两类合法形态：LookPath 探测（不 spawn 进程）与
//     cmd/argus-copilot（独立短命 CLI 二进制，进程寿命自缚，登记为 CLI 侧
//     第二执行面）。服务进程内的裸 spawn 一律迁 procx（#46 已清零 repomap 面）。
//
// 规则变更 = 架构变更：更新本文件白名单时须在 PR 描述记档（docs/architecture-deep-dive.md
// §4.2 执行面段同步）。测试只看 .Imports（非测试导入）：_test.go 里的 exec/procx
// 引用不在探针范围。
package arch

import (
	"os/exec"
	"strings"
	"testing"
)

// execPlaneProcxAllow procx 消费面白名单（2026-11-16 快照，12 文件 11 包；#50 实测）。
var execPlaneProcxAllow = map[string]bool{
	"github.com/yi-nology/argus/internal/fixverifier":         true, // fixverifier.go：验证命令执行
	"github.com/yi-nology/argus/internal/impactview":          true, // funcdiff/coupling/funcdiff_script：git/脚本对比
	"github.com/yi-nology/argus/internal/ocrdeleg":            true, // delegate.go：OCR 委托
	"github.com/yi-nology/argus/internal/ocrshim":             true, // run.go：sidecar shim
	"github.com/yi-nology/argus/internal/plugin/adapter_acpx": true, // acpx.go：CLI agent 适配
	"github.com/yi-nology/argus/internal/plugin/adapter_cli":  true, // cli.go：OCR cli 插件
	"github.com/yi-nology/argus/internal/runner":              true, // writing.go：修复链 git 收口（gitRun）
	"github.com/yi-nology/argus/internal/service":             true, // fix_render/fix_sandbox/sandbox_verify + repomap 克隆/探测（#46 迁入）
}

// execPlaneDirectExecAllow 直接 os/exec 白名单：仅 LookPath（不 spawn）与
// 登记 CLI 二进制。服务进程内新增直接 spawn = 探针失败，改走 procx。
var execPlaneDirectExecAllow = map[string]bool{
	"github.com/yi-nology/argus/internal/bootstrap":           true, // 仅 LookPath 探测插件二进制（不执行）
	"github.com/yi-nology/argus/internal/plugin/adapter_acpx": true, // 仅 LookPath 探测 agent CLI（不执行）
	"github.com/yi-nology/argus/cmd/argus-copilot":            true, // CLI 侧第二执行面（短命本地进程，登记非豁免：issue #50）
}

// TestExecPlaneSingleSource 执行面单源探针：全仓（internal + cmd + 根）非测试
// 导入面上扫 procx 与 os/exec 两个 import，逐包对白名单。扫描独立于 TestMain
// 的 deps（那里只采 internal/...），本探针覆盖 cmd/ 与模块根。
func TestExecPlaneSingleSource(t *testing.T) {
	out, err := exec.Command("go", "list", "-f",
		`{{.ImportPath}}|{{join .Imports "|"}}`, "github.com/yi-nology/argus/...").Output()
	if err != nil {
		t.Fatalf("go list 失败: %v: %s", err, string(out))
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		parts := strings.SplitN(line, "|", 2)
		if len(parts) != 2 {
			continue
		}
		pkg, imports := parts[0], parts[1]
		if imports == "" {
			continue
		}
		for imp := range strings.SplitSeq(imports, "|") {
			switch imp {
			case "github.com/oagentkit/runtime/procx":
				if !execPlaneProcxAllow[pkg] {
					t.Errorf("%s → procx：执行面白名单外包。procx 是服务进程外部命令的唯一纪律面"+
						"（进程组托管+管道收口）：确认适用后在 test/arch/exec_plane_test.go "+
						"execPlaneProcxAllow 显式登记，勿静默扩散（issue #50）", pkg)
				}
			case "os/exec":
				if !execPlaneDirectExecAllow[pkg] {
					t.Errorf("%s → os/exec：服务进程内直接 spawn 已禁（孙进程/管道不收口，issue #46/#50）"+
						"——改走 oagentkit/runtime/procx；确属 LookPath 探测或登记 CLI 二进制则扩 "+
						"execPlaneDirectExecAllow 白名单", pkg)
				}
			}
		}
	}
}
