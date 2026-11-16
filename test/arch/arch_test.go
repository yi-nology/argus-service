// Package arch 保留 archcheck 覆盖不到的架构守卫：包文档完整性。
//
// 分层依赖方向、未登记包、组合根单向性、裸 goroutine、os/exec 单一出口等
// 已由 `architecture-policy.yaml` + `cmd/archcheck`（`make arch`）单源执行；
// 本文件不再复制 layering 白名单——两份真相必然漂移（v7.3.x 重构批）。
package arch

import (
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
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

// ---- argus-no-printf-log-callback（#42，severity=error）----
//
// printf 风格日志回调一旦焊进公开 API 签名就是不可逆的 API 债；stdlib log 包
// 则绕开 logx 的级别/脱敏/trace 管线（FilterKey 凭证兜底只挂在 logx 装配口）。
// 本规则 severity=error（测试失败即非零退出）——warn 只进报告不阻断，等于没有。
// 存量已于本批清零（internal/ocrshim + cmd/ocrshim 共 6 处 stdlib log.Printf）。

// printfCallbackRe 「logger 回调」签名形态：名为 logf/logFunc/logFn/logger 的
// 参数，其类型是 func(...) 且首参为 string（首参允许带参数名——那才是 printf
// 回调的常见写法）。判据与 kernel archcheck 的 harness-no-printf-log-callback 同源。
var printfCallbackRe = regexp.MustCompile(`\b(logf|logFunc|logFn|logger)\s+func\(\s*(?:[A-Za-z_]\w*\s+)?string\b`)

// stdlibLogCallRe stdlib log 包留痕调用（Debug/Info/Warn/Error/Fatal/Print 全家族）。
// 判定以「本文件 import 了 stdlib log」为前提（见 TestNoPrintfLogCallback 的
// 两段式）：裸 `\blog\.` 分不清包限定与 receiver 字段——k.log.Warn(...）这类
// logx.Logger 结构体字段调用（memorykernel/argusctx 等 43 处）是合法形态。
var stdlibLogCallRe = regexp.MustCompile(`\blog\.(?:Debug|Info|Warn|Error|Fatal|Print)(?:f|ln)?\(`)

// stdlibLogImportRe stdlib log 包 import（含别名/空白导入；gorm.io/gorm/logger
// 等同名子包路径不命中）。
var stdlibLogImportRe = regexp.MustCompile(`^\s*(?:[_A-Za-z]\w*\s+)?"log"$`)

// TestNoPrintfLogCallback printf 风格日志禁令（#42，severity=error）：全仓 Go
// 源（生产 + 测试）扫两类形态——logger 回调签名（独立判定）；stdlib log 留痕
// （两段式：先判 import，命中才报调用行——receiver 风格 .log.X(...) 的 logx
// 字段调用不误伤）。留痕统一 logx.Logger：进程服务面由 main 装配默认后端
// （logx.SetDefault），SDK/旁路进程包级 logger 用 logx.Delegating 懒解析。
func TestNoPrintfLogCallback(t *testing.T) {
	const repoRoot = "../.." // go test 的 CWD = 本包目录
	const rootPrefix = repoRoot + "/"
	var hits []string
	walkErr := filepath.WalkDir(repoRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", ".hz", "node_modules", "bin":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		display := strings.TrimPrefix(path, rootPrefix)
		var importsStdlibLog bool
		var callHits []string
		for i, line := range strings.Split(string(data), "\n") {
			if printfCallbackRe.MatchString(line) {
				hits = append(hits, fmt.Sprintf("%s:%d printf 风格日志回调签名：留痕改走 logx.Logger（SDK 包级用 logx.Delegating）", display, i+1))
			}
			if stdlibLogImportRe.MatchString(line) {
				importsStdlibLog = true
				hits = append(hits, fmt.Sprintf("%s:%d stdlib log 包 import：留痕统一 logx.Logger", display, i+1))
			}
			if stdlibLogCallRe.MatchString(line) {
				callHits = append(callHits, fmt.Sprintf("%s:%d stdlib log 直呼：留痕统一 logx.Logger", display, i+1))
			}
		}
		if importsStdlibLog {
			hits = append(hits, callHits...)
		}
		return nil
	})
	if walkErr != nil {
		t.Fatalf("printf-log 扫描失败: %v", walkErr)
	}
	for _, h := range hits {
		t.Errorf("argus-no-printf-log-callback（error）: %s", h)
	}
}
