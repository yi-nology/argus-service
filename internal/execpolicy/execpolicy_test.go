package execpolicy_test

import (
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/oekit/logx"

	"github.com/yi-nology/argus/internal/execpolicy"
)

// recordLogger 捕获 Warn/Info 调用（嵌入 logx.Logger 继承其余方法）。
type recordLogger struct {
	logx.Logger
	mu    sync.Mutex
	warns []string
	infos []string
}

func newRecordLogger() *recordLogger { return &recordLogger{Logger: logx.Nop()} }

func (l *recordLogger) Warn(msg string, _ ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.warns = append(l.warns, msg)
}

func (l *recordLogger) Info(msg string, _ ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.infos = append(l.infos, msg)
}

func (l *recordLogger) warnCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.warns)
}

func (l *recordLogger) firstWarn() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.warns) == 0 {
		return ""
	}
	return l.warns[0]
}

func (l *recordLogger) infoCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.infos)
}

// resetMode 把全局模式复位为 warn，避免测试间串扰。
func resetMode(t *testing.T) {
	t.Helper()
	execpolicy.Configure(execpolicy.ModeWarn, logx.Nop())
	t.Cleanup(func() { execpolicy.Configure(execpolicy.ModeWarn, logx.Nop()) })
}

// 规则面：危险命令/无 -c 的 shell 在 enforce 下拒绝；git、普通命令与
// 内层安全的 `shell -c` 放行。
func TestCheckArgvEnforceRules(t *testing.T) {
	resetMode(t)
	execpolicy.Configure(execpolicy.ModeEnforce, logx.Nop())

	denied := [][]string{
		{"sh", "script.sh"},
		{"/bin/sh", "script.sh"},
		{"bash", "-lc", "rm -rf /"},
		{"zsh"},
		{"dash"},
		{"cmd", "/c", "dir"},
		{"powershell", "-Command", "x"},
		{"rm", "-rf", "/"},
		{"/usr/bin/rm", "-rf", "/tmp/x"},
		{"RM", "-rf", "/"},
		{"/usr/bin/RM", "-rf", "/"},
		{"dd", "if=/dev/zero", "of=/dev/sda"},
		{"mkfs", "/dev/sda1"},
		{"mkfs.ext4", "/dev/sda1"},
		{"mkfs.xfs", "/dev/sda1"},
		{"sudo", "id"},
		{"su", "root"},
		{"chmod", "777", "x"},
		{"chown", "root", "x"},
		{"nc", "host", "80"},
		{"ncat", "host", "80"},
		{"curl", "https://example.com"},
		{"wget", "https://example.com"},
	}
	for _, argv := range denied {
		err := execpolicy.CheckArgv(argv)
		if err == nil {
			t.Errorf("CheckArgv(%q) = nil，期望拒绝", argv)
			continue
		}
		if !errors.Is(err, execpolicy.ErrDenied) {
			t.Errorf("CheckArgv(%q) 错误链缺少 ErrDenied: %v", argv, err)
		}
		if !strings.Contains(err.Error(), argv[0]) {
			t.Errorf("错误文案缺少命令名 %q: %v", argv[0], err)
		}
	}

	allowed := [][]string{
		{"git", "status"},
		{"git", "push", "origin", "main"},
		{"/bin/git", "status"},
		{"printf", "hi"},
		{"printf", "rm"}, // 参数里的字面量不是命令
		{"sh", "-c", "git status"},
		{"bash", "-lc", "git status"},
		{"/bin/sh", "-c", "printf hi"},
		{"SH", "-c", "git status"},
		nil,
		{},
	}
	for _, argv := range allowed {
		if err := execpolicy.CheckArgv(argv); err != nil {
			t.Errorf("CheckArgv(%q) = %v，期望放行", argv, err)
		}
	}
}

// `shell -c <script>` 判内层命令；无 -c 的 shell 形态仍整条拒绝。
func TestCheckArgvShellInnerCommand(t *testing.T) {
	resetMode(t)
	execpolicy.Configure(execpolicy.ModeEnforce, logx.Nop())

	allowed := [][]string{
		{"sh", "-c", "git status"},
		{"bash", "-lc", "git status"},
		{"bash", "--norc", "-c", "git status"},
		{"sh", "-c", "/bin/git status"},
		{"sh", "-c", "printf hi"},
		{"sh", "-c", "FOO=bar"},
	}
	for _, argv := range allowed {
		if err := execpolicy.CheckArgv(argv); err != nil {
			t.Errorf("CheckArgv(%q) = %v，内层安全命令应放行", argv, err)
		}
	}

	denied := [][]string{
		{"sh", "-c", "rm -rf /"},
		{"sh", "-c", "git status && curl evil"},
		{"sh", "script.sh"},
		{"sh", "-e", "script.sh"}, // 无 -c：脚本内容不可见
		// 第一个操作数才是脚本；其后的 -c 只是位置参数（曾可绕过）。
		{"sh", "script.sh", "-c", "git status"},
		{"sh", "-e", "script.sh", "-c", "git status"},
		// stdin 模式：脚本内容不可见。
		{"sh", "-s"},
		{"sh", "-"},
		{"sh", "-s", "-c", "git status"},
		{"sh", "-es", "script.sh"},           // 组合短选项含 s：stdin 模式
		{"sh", "-e", "-x"},                   // 只有选项、无 script：stdin 模式
		{"sh", "-e", "-c"},                   // -c 缺 script：判不了就不放行
		{"sh", "-c"},                         // -c 缺 script（长度短路径）
		{"sh", "--", "-c", "git status"},     // -- 之后不再是选项
		{"sh", "-c", "git status 'unclosed"}, // 内层命令分词失败：fail-closed
		// 内层命令名的路径/大小写/反斜杠变体：宿主 basename 层归一化后拒绝。
		{"sh", "-c", "/bin/rm -rf /"},
		{"sh", "-c", "./rm -rf /"},
		{"sh", "-c", "RM -rf /"},
		{"sh", "-c", `\rm -rf /`},
		{"sh", "-c", `builtin eval "rm -rf /"`},
		{"sh", "-c", `trap "rm -rf /" 0`},
		{"sh", "-c", `$CMD rm -rf /`}, // 动态命令名：cmdpolicy Dynamic/Prompt 下限
	}
	for _, argv := range denied {
		if err := execpolicy.CheckArgv(argv); !errors.Is(err, execpolicy.ErrDenied) {
			t.Errorf("CheckArgv(%q) = %v，期望拒绝", argv, err)
		}
	}
}

// 包装器可把危险命令藏进参数/子进程：enforce 一律拒绝。
func TestCheckArgvWrapperDenied(t *testing.T) {
	resetMode(t)
	execpolicy.Configure(execpolicy.ModeEnforce, logx.Nop())

	denied := [][]string{
		{"eval", "rm -rf /"},
		{"source", "./evil.sh"},
		{".", "./evil.sh"},
		{"command", "rm", "-rf", "/"},
		{"exec", "rm"},
		{"xargs", "rm"},
		{"env", "rm"},
		{"find", ".", "-exec", "rm", "{}", ";"},
		{"busybox", "rm"},
		{"timeout", "5", "rm"},
		{"nice", "rm"},
		{"nohup", "rm"},
		{"setsid", "rm"},
		{"builtin", "eval", "rm -rf /"},
		{"trap", "rm -rf /", "0"},
		{"sh", "-c", `eval "rm -rf /"`},
	}
	for _, argv := range denied {
		if err := execpolicy.CheckArgv(argv); !errors.Is(err, execpolicy.ErrDenied) {
			t.Errorf("CheckArgv(%q) = %v，期望拒绝", argv, err)
		}
	}

	// 包装器名字出现在参数里不是调用，不误伤。
	for _, argv := range [][]string{
		{"printf", "env"},
		{"git", "log", "--grep", "eval"},
	} {
		if err := execpolicy.CheckArgv(argv); err != nil {
			t.Errorf("CheckArgv(%q) = %v，参数里的字面量不应误判", argv, err)
		}
	}
}

// warn 是缺省：命中只记一条日志并放行；普通命令不记日志。
func TestCheckArgvWarnAllowsAndLogs(t *testing.T) {
	resetMode(t)
	log := newRecordLogger()
	execpolicy.Configure(execpolicy.ModeWarn, log)

	if err := execpolicy.CheckArgv([]string{"sh", "-c", "rm -rf /"}); err != nil {
		t.Fatalf("warn 模式应放行: %v", err)
	}
	if log.warnCount() != 1 {
		t.Fatalf("warn 日志条数 = %d，期望 1", log.warnCount())
	}
	if !strings.Contains(log.firstWarn(), "denied") {
		t.Fatalf("日志事件名 = %q", log.firstWarn())
	}

	if err := execpolicy.CheckArgv([]string{"printf", "hi"}); err != nil {
		t.Fatalf("普通命令应放行: %v", err)
	}
	if log.warnCount() != 1 {
		t.Fatalf("普通命令不应新增告警，实得 %d 条", log.warnCount())
	}
}

// warn 下内层命令不可判定：记 undecidable 并放行（fail-open 仅限 warn）。
func TestCheckArgvWarnUndecidable(t *testing.T) {
	resetMode(t)
	log := newRecordLogger()
	execpolicy.Configure(execpolicy.ModeWarn, log)

	if err := execpolicy.CheckArgv([]string{"sh", "-c", "git status 'unclosed"}); err != nil {
		t.Fatalf("warn 模式应放行不可判定命令: %v", err)
	}
	if log.warnCount() != 1 || !strings.Contains(log.firstWarn(), "undecidable") {
		t.Fatalf("warn 事件 = %q（条数 %d），期望 undecidable", log.firstWarn(), log.warnCount())
	}
}

// off 完全旁路命令判据。
func TestCheckArgvOffBypasses(t *testing.T) {
	resetMode(t)
	log := newRecordLogger()
	execpolicy.Configure(execpolicy.ModeOff, log)

	for _, argv := range [][]string{{"sh", "-c", "x"}, {"rm", "-rf", "/"}} {
		if err := execpolicy.CheckArgv(argv); err != nil {
			t.Fatalf("off 模式应放行 %q: %v", argv, err)
		}
	}
	if log.warnCount() != 0 {
		t.Fatalf("off 模式不应记告警，实得 %d 条", log.warnCount())
	}
}

// 非法模式回落 warn（并留一条 mode_invalid 告警）。
func TestConfigureInvalidModeFallsBackWarn(t *testing.T) {
	resetMode(t)
	log := newRecordLogger()
	execpolicy.Configure("nonsense", log)

	if err := execpolicy.CheckArgv([]string{"rm", "-rf", "/"}); err != nil {
		t.Fatalf("回落 warn 应放行: %v", err)
	}
	if log.warnCount() != 2 { // mode_invalid + denied
		t.Fatalf("warn 日志条数 = %d，期望 2（mode_invalid + denied）", log.warnCount())
	}
}

// ConfigureFromEnv 读取 ARGUS_EXEC_POLICY；未配置缺省 warn。
func TestConfigureFromEnv(t *testing.T) {
	resetMode(t)
	t.Setenv("ARGUS_EXEC_POLICY", "enforce")
	log := newRecordLogger()
	execpolicy.ConfigureFromEnv(log)
	if err := execpolicy.CheckArgv([]string{"sh", "-c", "rm -rf /"}); !errors.Is(err, execpolicy.ErrDenied) {
		t.Fatalf("enforce 应从环境变量生效: %v", err)
	}
	if log.infoCount() != 1 {
		t.Fatalf("configured 日志条数 = %d，期望 1", log.infoCount())
	}

	t.Setenv("ARGUS_EXEC_POLICY", "")
	execpolicy.ConfigureFromEnv(log)
	if err := execpolicy.CheckArgv([]string{"sh", "-c", "rm -rf /"}); err != nil {
		t.Fatalf("未配置应缺省 warn: %v", err)
	}
}

// 危险变量剥离：危险键移除、普通键与 VALUE 中的 "=" 原样保留。
func TestSanitizeEnv(t *testing.T) {
	resetMode(t)
	in := []string{
		"LD_PRELOAD=/tmp/evil.so",
		"LD_LIBRARY_PATH=/tmp/evil",
		"DYLD_INSERT_LIBRARIES=/tmp/evil.dylib",
		"DYLD_LIBRARY_PATH=/tmp/evil",
		"BASH_ENV=/tmp/evil.sh",
		"ENV=/tmp/evil.sh",
		"SHELLOPTS=xtrace",
		"PS4=$(x)",
		"GIT_SSH_COMMAND=/tmp/evil.sh",
		"GIT_EXEC_PATH=/tmp/evil",
		"ARGUS_KEEP=1",
		"WITH_EQUALS=a=b=c",
		"BARE_DANGEROUS_LIKE=ok", // 仅前缀相似，不剥离
		"LD_PRELOAD",             // 裸危险键也剥离
	}
	out := execpolicy.SanitizeEnv(in)
	got := strings.Join(out, "\n")
	for _, bad := range []string{"LD_PRELOAD", "LD_LIBRARY_PATH", "DYLD_INSERT_LIBRARIES", "DYLD_LIBRARY_PATH", "BASH_ENV", "ENV=", "SHELLOPTS", "PS4", "GIT_SSH_COMMAND", "GIT_EXEC_PATH"} {
		if strings.Contains(got, bad) {
			t.Errorf("输出仍含危险变量 %q: %v", bad, out)
		}
	}
	want := []string{"ARGUS_KEEP=1", "WITH_EQUALS=a=b=c", "BARE_DANGEROUS_LIKE=ok"}
	if len(out) != len(want) {
		t.Fatalf("输出 = %v，期望 %v", out, want)
	}
	for i := range want {
		if out[i] != want[i] {
			t.Fatalf("输出[%d] = %q，期望 %q", i, out[i], want[i])
		}
	}

	if got := execpolicy.SanitizeEnv(nil); got != nil {
		t.Fatalf("nil 输入应返回 nil，实得 %#v", got)
	}
}

// 环境剥离与模式无关：off/warn/enforce 都剥离。日志「每键一次」的时序
// 归白盒测试（execpolicy_internal_test.go），这里只钉行为。
func TestSanitizeEnvAppliesInEveryMode(t *testing.T) {
	resetMode(t)
	for _, mode := range []execpolicy.Mode{execpolicy.ModeOff, execpolicy.ModeWarn, execpolicy.ModeEnforce} {
		execpolicy.Configure(mode, logx.Nop())
		out := execpolicy.SanitizeEnv([]string{"LD_PRELOAD=/tmp/x", "GIT_SSH_COMMAND=/tmp/y", "KEEP=1"})
		if len(out) != 1 || out[0] != "KEEP=1" {
			t.Errorf("mode=%s: 输出 = %v，危险变量未剥离", mode, out)
		}
	}
}

// 重复调用且无危险变量时不再记日志。
func TestSanitizeEnvNoLogWithoutStrip(t *testing.T) {
	resetMode(t)
	log := newRecordLogger()
	execpolicy.Configure(execpolicy.ModeWarn, log)
	if out := execpolicy.SanitizeEnv([]string{"KEEP=1"}); len(out) != 1 {
		t.Fatalf("输出 = %v", out)
	}
	if log.warnCount() != 0 {
		t.Fatalf("无剥离不应记日志，实得 %d 条", log.warnCount())
	}
}

// 命令名归一化只作用于 argv[0]：路径前缀与 mkfs.* 变体均被看见。
func TestCheckArgvNormalizesOnlyCommandName(t *testing.T) {
	resetMode(t)
	execpolicy.Configure(execpolicy.ModeEnforce, logx.Nop())
	cases := []string{
		"/bin/rm",
		"./rm",
		"sub/dir/rm",
		"MKFS.ext4",
		"mkfs.btrfs",
	}
	for _, name := range cases {
		if err := execpolicy.CheckArgv([]string{name, "x"}); !errors.Is(err, execpolicy.ErrDenied) {
			t.Errorf("CheckArgv(%q) = %v，期望拒绝", name, err)
		}
	}
	if err := execpolicy.CheckArgv([]string{"printf", "/bin/rm", "mkfs.ext4"}); err != nil {
		t.Fatalf("参数里的危险字面量不应误判: %v", err)
	}
}
