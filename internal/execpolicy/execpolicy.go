// Package execpolicy 是 argus 子进程执行策略：命令级拒绝 + 危险环境变量剥离。
//
// # 边界：这是策略，不是沙箱
//
// 本包只做两件真实可落地的事：
//
//   - 用 kernel cmdpolicy 的前缀规则拒绝 shell 与危险命令（rm/dd/mkfs*/sudo/
//     curl/wget …），规则自带 Match/NotMatch 样例，加载期自检；
//   - 从子进程环境里剥离可劫持加载器/解释器/git 的危险变量（LD_PRELOAD、
//     DYLD_INSERT_LIBRARIES、GIT_SSH_COMMAND …），任何模式下都剥离。
//
// **不**提供容器/VM 隔离、文件系统/网络命名空间或出口代理——把策略当沙箱
// 会给出"看似安全"的假象。backend/gatecheck/preset/egress 未采用，见 issue #37。
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
// # 判定面
//
// 判据只看 argv（调用方已分词），不做 shell 解析：execx 的 WithShell 入口
// 也先展开成 "/bin/sh -c …" 再进判据，因此 shell 形态同样被看见。
// argv[0] 取 basename（/bin/rm → rm），mkfs.* 归一为 mkfs（对应规则里的
// mkfs* 语义）。未命中任何规则 = 放行：本切片是危险命令拒绝，不是全量
// allowlist。
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

// dangerousEnvKeys 子进程不得继承的环境变量：加载器/解释器/git 的注入面。
// 任何模式下都剥离（含 off）。
var dangerousEnvKeys = []string{
	"LD_PRELOAD",
	"LD_LIBRARY_PATH",
	"DYLD_INSERT_LIBRARIES",
	"DYLD_LIBRARY_PATH",
	"BASH_ENV",
	"ENV",
	"SHELLOPTS",
	"PS4",
	"GIT_SSH_COMMAND",
	"GIT_EXEC_PATH",
}

// dangerousEnvSet 是 dangerousEnvKeys 的查找集（避免每次线性扫描）。
var dangerousEnvSet = func() map[string]struct{} {
	m := make(map[string]struct{}, len(dangerousEnvKeys))
	for _, k := range dangerousEnvKeys {
		m[k] = struct{}{}
	}
	return m
}()

// settings 一次配置的不可变快照；整体原子替换，避免读写撕裂。
type settings struct {
	mode Mode
	log  logx.Logger
}

// defaultSettings 未显式配置时的缺省：warn（记录但放行）。
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
func CheckArgv(argv []string) error {
	s := loadSettings()
	if s.mode == ModeOff || len(argv) == 0 {
		return nil
	}
	v := policy.Decide(normalizeArgv(argv))
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

// SanitizeEnv 剥离危险环境变量，保留其余 KEY=VALUE 项与原顺序。
//
// 与模式无关（任何模式都剥离）；发生剥离时记一条 Warn（列出被剥离的键）。
// 无 "=" 的条目按裸键判定：危险的裸键同样丢弃，其余保留。
func SanitizeEnv(env []string) []string {
	if env == nil {
		return nil
	}
	out := make([]string, 0, len(env))
	var stripped []string
	for _, entry := range env {
		key := entry
		if i := strings.IndexByte(entry, '='); i >= 0 {
			key = entry[:i]
		}
		if _, bad := dangerousEnvSet[key]; bad {
			stripped = append(stripped, key)
			continue
		}
		out = append(out, entry)
	}
	if len(stripped) > 0 {
		if log := loadSettings().log; log != nil {
			log.Warn("argus.execpolicy.env_stripped", "vars", strings.Join(stripped, ","))
		}
	}
	return out
}

// normalizeArgv 归一命令名，使规则只登记稳定形态：
//   - 取 basename：/bin/rm、./rm → rm；
//   - mkfs.* → mkfs（规则里以 mkfs 表达 mkfs* 语义）。
//
// 只改 argv[0]（命令名）；参数原样保留，避免把参数里的字面量误判成命令。
func normalizeArgv(argv []string) []string {
	if len(argv) == 0 {
		return argv
	}
	out := make([]string, len(argv))
	copy(out, argv)
	base := filepath.Base(out[0])
	if strings.HasPrefix(strings.ToLower(base), "mkfs") {
		base = "mkfs"
	}
	out[0] = base
	return out
}

// policy 是加载期已自检的前缀规则集（构造后只读，可并发共享）。
var policy = mustPolicy()

// mustPolicy 构造默认规则；规则写错是编程错误，启动期 panic 优于静默降级。
func mustPolicy() *cmdpolicy.Policy {
	p, err := cmdpolicy.New([]cmdpolicy.Rule{
		{
			Name:     "deny-shell",
			Prefix:   cmdpolicy.Prefix{{"sh", "bash", "zsh", "dash", "cmd", "powershell"}},
			Decision: cmdpolicy.DecisionForbidden,
			Justification: "argus 子进程不得经 shell 执行（引号/展开/注入面）；" +
				"请把命令拆成 argv 直传",
			Match:    []string{"sh -c true", "bash -lc true", "zsh -c true", "dash -c true", "cmd /c dir", "powershell -Command x"},
			NotMatch: []string{"git status", "printf hi"},
		},
		{
			Name:     "deny-dangerous",
			Prefix:   cmdpolicy.Prefix{{"rm", "dd", "mkfs", "sudo", "su", "chmod", "chown", "nc", "ncat", "curl", "wget"}},
			Decision: cmdpolicy.DecisionForbidden,
			Justification: "危险命令（破坏性/提权/权限修改/网络外联）不得由 argus 子进程发起；" +
				"mkfs.* 归一为 mkfs 后同判",
			Match: []string{
				"rm -rf /", "dd if=/dev/zero of=/dev/sda", "mkfs /dev/sda1",
				"sudo id", "su root", "chmod 777 x", "chown root x",
				"nc host 80", "ncat host 80", "curl https://x", "wget https://x",
			},
			NotMatch: []string{"git status", "printf hi"},
		},
		{
			Name:          "allow-git",
			Prefix:        cmdpolicy.Prefix{{"git"}},
			Decision:      cmdpolicy.DecisionAllow,
			Justification: "argus 的 git 读写/推送链（显式放行，便于报告解释）",
			Match:         []string{"git status", "git push origin main"},
			NotMatch:      []string{"printf hi", "rm -rf /"},
		},
	})
	if err != nil {
		panic("execpolicy: 默认规则自检失败: " + err.Error())
	}
	return p
}
