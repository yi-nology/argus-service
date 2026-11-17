// Package execpolicy 是 argus 子进程执行策略：命令级拒绝 + 危险环境变量剥离。
//
// # 边界：这是策略，不是沙箱
//
// 本包只做两件真实可落地的事：
//
//   - 用 kernel cmdpolicy 的前缀规则拒绝 shell、危险命令（rm/dd/mkfs*/sudo/
//     curl/wget …）与可藏命令的包装器（eval/source/exec/xargs/env/find …），
//     规则自带 Match/NotMatch 样例，加载期自检；
//   - 从子进程环境里剥离可劫持加载器/解释器/git 的危险变量（LD_PRELOAD、
//     DYLD_INSERT_LIBRARIES、GIT_SSH_COMMAND …），任何模式下都剥离。
//
// **不**提供容器/VM 隔离、文件系统/网络命名空间或出口代理——把策略当沙箱
// 会给出"看似安全"的假象。backend/gatecheck/preset/egress 的执行面装配在
// internal/acpxsandbox（#37）；那五项同样不把进程级参数包装成容器隔离。
//
// # 三态模式
//
// ARGUS_EXEC_POLICY=off|warn|enforce，缺省 warn：
//
//   - warn：命中危险命令时记 Warn 并放行（首版不改变线上行为）；
//   - enforce：命中即返回 ErrDenied（execx 转成 *execx.Error，ExitCode -1）；
//   - off：完全旁路命令判据（环境剥离仍生效）。
//
// 环境变量剥离与模式无关：即使 off/warn 也剥离——它是"继承环境"的安全默认，
// 不是命令判据的一部分。
//
// # 判定面（两层，取更严）
//
//  1. 宿主侧危险 basename 集：对 argv[0] 与 `-c` 脚本 AST 里的每个命令调用
//     （syntax.CallExpr 的首个 Word）做 TrimLeft("\\") + filepath.Base +
//     ToLower，再查危险集（含 mkfs* 前缀）。路径（/bin/rm、./rm）、大小写
//     （RM）与反斜杠（\rm）变体在这一层被归一化，不依赖 cmdpolicy 的字面 token。
//  2. cmdpolicy 前缀规则 + AST 事实面：shell/危险命令/包装器 Forbidden；
//     动态展开/写重定向抬到 Prompt；enforce 下 Forbidden/Prompt 都拒绝。
//
// 对 POSIX shell 的 `-c <script>`，内层命令走第 2 层，同时第 1 层扫描脚本
// AST 的每个调用。没有 `-c` 的 shell 形态（sh script.sh、cmd/powershell）整条
// 拒绝——脚本文件内容在判定时不可见，不能假装判过。
//
// 参数原样保留。未命中任何规则 = 放行：这是危险命令拒绝（deny-list），不是
// 全量 allowlist。
//
// # enforce 的已知限制（部署前必读）
//
//   - `-c` 脚本含动态展开（`$VAR`/`$()`）或写重定向：cmdpolicy 的事实面
//     会把裁决下限抬到 Prompt，enforce 下拒绝。部署方的 fix/verify 模板
//     必须是静态、无重定向的命令（或改为 argv 直传）。
//   - basename 集与包装器（eval/source/./command/exec/xargs/env/find/
//     busybox/timeout/nice/nohup/setsid/builtin/trap）整条拒绝：它们能把
//     危险命令藏在参数或子进程里。
//   - 这是 basename denylist + cmdpolicy，**不是沙箱**：改名/自编译二进制、
//     未列出的包装器、拼接构造的命令名仍可能绕过；文件系统/网络隔离仍需
//     容器/VM 后端（C4 剩余项，见 issue #37）。
package execpolicy

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"

	"github.com/oekit/logx"
	"github.com/yi-nology/goagent-kernel/cmdpolicy"
)

// Mode 执行策略模式。
type Mode string

const (
	// ModeOff 完全旁路命令判据（仅测试/紧急回滚用；环境剥离仍生效）。
	ModeOff Mode = "off"
	// ModeWarn 记录违规但放行（缺省；首版不改变线上行为）。
	ModeWarn Mode = "warn"
	// ModeEnforce 拒绝违规命令。
	ModeEnforce Mode = "enforce"
)

// envMode 是模式开关的环境变量名。
const envMode = "ARGUS_EXEC_POLICY"

// ErrDenied 是命令被策略拒绝的 sentinel（errors.Is 可判）。
var ErrDenied = errors.New("execpolicy: 命令被拒绝")

// settings 一次配置的不可变快照；整体原子替换，避免读写撕裂。
type settings struct {
	mode Mode
	log  logx.Logger
}

// defaultSettings 未显式配置时的缺省：warn（记录但放行）。logger 为 nil 时
// 日志调用是 no-op（bootstrap 总会先 ConfigureFromEnv，测试可直接用缺省）。
var defaultSettings = &settings{mode: ModeWarn}

var current atomic.Pointer[settings]

// loadSettings 取当前快照（永远非 nil）。
func loadSettings() *settings {
	if s := current.Load(); s != nil {
		return s
	}
	return defaultSettings
}

// Configure 设置全局模式（测试入口）。非法模式回落 warn 并记一条告警。
func Configure(mode Mode, log logx.Logger) {
	m := Mode(strings.ToLower(strings.TrimSpace(string(mode))))
	switch m {
	case ModeOff, ModeWarn, ModeEnforce:
	default:
		if log != nil {
			log.Warn("argus.execpolicy.mode_invalid", "mode", string(mode), "fallback", string(ModeWarn))
		}
		m = ModeWarn
	}
	current.Store(&settings{mode: m, log: log})
}

// ConfigureFromEnv 从 ARGUS_EXEC_POLICY 装配全局策略；bootstrap 装配早期调用
// （与 outbound 同序）。未配置时缺省 warn。
func ConfigureFromEnv(log logx.Logger) {
	mode := Mode(strings.TrimSpace(os.Getenv(envMode)))
	if mode == "" {
		mode = ModeWarn
	}
	Configure(mode, log)
	if log != nil {
		log.Info("argus.execpolicy.configured", "mode", string(loadSettings().mode))
	}
}

// CheckArgv 对一次子进程调用做命令级判据。
//
// argv 是调用方已分好词的参数（argv[0] 为命令名）。返回 nil 表示放行：
// 未命中规则、warn 模式命中、off 模式都返回 nil；enforce 模式命中返回
// 包装 ErrDenied 的错误（错误文案含命令名、规则名与理由）。
//
// shell 调用的判定面：POSIX shell 的 `-c <script>` 判 script 的内层命令
// （Allow 放行、Forbidden/Prompt 拒绝）；内层命令无法解析时 fail-closed
// （enforce 拒绝、warn 记录后放行）。无 `-c` 的 shell 形态整条拒绝。
func CheckArgv(argv []string) error {
	s := loadSettings()
	if s.mode == ModeOff || len(argv) == 0 {
		return nil
	}
	v, err := decide(argv)
	if err != nil {
		if s.mode == ModeEnforce {
			return fmt.Errorf("%w：命令 %q（%v）", ErrDenied, argv[0], err)
		}
		if s.log != nil {
			s.log.Warn("argus.execpolicy.undecidable",
				"command", argv[0], "reason", err.Error(), "mode", string(s.mode))
		}
		return nil
	}
	// 本切片只登记 allow/forbidden；prompt 仍按「不静默放行」处理。
	if v.Decision < cmdpolicy.DecisionPrompt {
		return nil
	}
	if s.mode == ModeEnforce {
		return fmt.Errorf("%w：命令 %q（%s）", ErrDenied, argv[0], v.Explain())
	}
	if s.log != nil {
		s.log.Warn("argus.execpolicy.denied",
			"command", argv[0], "decision", v.Explain(), "mode", string(s.mode))
	}
	return nil
}

// decide 返回 argv 的裁决：宿主侧 basename 集与 cmdpolicy 规则取更严。
func decide(argv []string) (cmdpolicy.Verdict, error) {
	// 第 1 层：外层命令名归一化后查危险 basename 集（路径/大小写/反斜杠变体）。
	if len(argv) > 0 && dangerousBasename(argv[0]) {
		return basenameForbidden(argv[0]), nil
	}
	norm := normalizeArgv(argv)
	script, ok := shellScript(norm)
	if !ok {
		// 第 2 层：cmdpolicy 前缀规则（无 -c 的 shell 在此整条拒绝）。
		return policy.Decide(norm), nil
	}
	// 第 2 层：cmdpolicy 的 AST 全调用面。
	v, err := policy.DecideCommand(script)
	if err != nil {
		return cmdpolicy.Verdict{}, fmt.Errorf("shell -c 内层命令不可判定: %w", err)
	}
	// 第 1 层：脚本 AST 的每个命令调用都做 basename 归一化检查。
	bv, err := innerBasenameVerdict(script)
	if err != nil {
		return cmdpolicy.Verdict{}, err
	}
	if bv.Decision > v.Decision {
		return bv, nil
	}
	return v, nil
}

// posixShells 是支持 `-c <script>` 内层判定的 shell。cmd/powershell 的
// 命令语法不属于 POSIX 分词面，仍整条拒绝。
var posixShells = map[string]struct{}{
	"sh": {}, "bash": {}, "zsh": {}, "dash": {},
}

// shellScript 识别 POSIX shell 的 `-c <script>` 形态并返回 script。
// 组合短选项（bash -lc）同样识别；`--` 之后不再当选项。缺少 script 时
// 返回 false，落到整条 deny-shell（判不了就不放行）。
//
// 关键：遇到第一个非选项操作数就停止——它才是 shell 要执行的脚本文件
// （`sh script.sh -c …` 里的 -c 只是位置参数）。继续扫描会把「执行文件」
// 误判成「执行 -c 字符串」，从而放行任意脚本。
func shellScript(argv []string) (string, bool) {
	if len(argv) < 3 {
		return "", false
	}
	if _, ok := posixShells[argv[0]]; !ok {
		return "", false
	}
	for i := 1; i < len(argv); i++ {
		a := argv[i]
		switch {
		case a == "--":
			// 选项终止：之后全是操作数，无法确认执行的是 -c 脚本。
			return "", false
		case a == "-" || a == "-s":
			// stdin 模式：脚本来自标准输入，内容不可见。
			return "", false
		case !strings.HasPrefix(a, "-"):
			// 第一个操作数：它才是脚本文件，后面的 -c 只是位置参数。
			return "", false
		case strings.HasPrefix(a, "--"):
			// 长选项（--norc 等）继续扫描。
			continue
		}
		if strings.ContainsRune(a[1:], 'c') {
			if i+1 >= len(argv) {
				return "", false
			}
			return argv[i+1], true
		}
		if strings.ContainsRune(a[1:], 's') {
			// -s / -es 等 stdin 模式：脚本内容不可见。
			return "", false
		}
	}
	return "", false
}

// normalizeBasename 归一命令名：去掉前导反斜杠（shell 转义/Windows 习惯）、
// 取路径 basename、转小写。macOS 文件系统大小写不敏感：RM 与 rm 是同一个
// 可执行文件；/bin/rm、./rm、\rm 也都是同一条命令。
func normalizeBasename(name string) string {
	return strings.ToLower(filepath.Base(strings.TrimLeft(name, "\\")))
}

// dangerousBasename 判断命令名归一化后是否命中危险 basename 集；mkfs 变体
// （mkfs.ext4/mkfs.xfs …）按 mkfs* 前缀同判。
func dangerousBasename(name string) bool {
	base := normalizeBasename(name)
	if strings.HasPrefix(base, "mkfs") {
		return true
	}
	_, bad := dangerousBasenames[base]
	return bad
}

// normalizeArgv 归一命令名，使 cmdpolicy 的 token 规则只登记稳定形态：
// basename + 小写（见 normalizeBasename）。mkfs 变体由 basename 层的
// mkfs* 前缀直接拒绝，不需要再映射成单一 token。
//
// 只改 argv[0]（命令名）；参数原样保留，避免把参数里的字面量误判成命令。
func normalizeArgv(argv []string) []string {
	if len(argv) == 0 {
		return argv
	}
	out := make([]string, len(argv))
	copy(out, argv)
	out[0] = normalizeBasename(out[0])
	return out
}
