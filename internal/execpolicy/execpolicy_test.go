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

// 规则面：危险命令/ shell 在 enforce 下全部拒绝，git 与普通命令放行。
func TestCheckArgvEnforceRules(t *testing.T) {
	resetMode(t)
	execpolicy.Configure(execpolicy.ModeEnforce, logx.Nop())

	denied := [][]string{
		{"sh", "-c", "true"},
		{"/bin/sh", "-c", "true"},
		{"bash", "-lc", "true"},
		{"zsh"},
		{"dash"},
		{"cmd", "/c", "dir"},
		{"powershell", "-Command", "x"},
		{"rm", "-rf", "/"},
		{"/usr/bin/rm", "-rf", "/tmp/x"},
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
		{"printf", "hi"},
		{"printf", "rm"}, // 参数里的字面量不是命令
		{"env"},
		nil,
		{},
	}
	for _, argv := range allowed {
		if err := execpolicy.CheckArgv(argv); err != nil {
			t.Errorf("CheckArgv(%q) = %v，期望放行", argv, err)
		}
	}
}

// warn 是缺省：命中只记一条日志并放行；普通命令不记日志。
func TestCheckArgvWarnAllowsAndLogs(t *testing.T) {
	resetMode(t)
	log := newRecordLogger()
	execpolicy.Configure(execpolicy.ModeWarn, log)

	if err := execpolicy.CheckArgv([]string{"sh", "-c", "x"}); err != nil {
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
	if err := execpolicy.CheckArgv([]string{"sh", "-c", "x"}); !errors.Is(err, execpolicy.ErrDenied) {
		t.Fatalf("enforce 应从环境变量生效: %v", err)
	}
	if log.infoCount() != 1 {
		t.Fatalf("configured 日志条数 = %d，期望 1", log.infoCount())
	}

	t.Setenv("ARGUS_EXEC_POLICY", "")
	execpolicy.ConfigureFromEnv(log)
	if err := execpolicy.CheckArgv([]string{"sh", "-c", "x"}); err != nil {
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

// 环境剥离与模式无关：off/warn/enforce 都剥离；剥离时只记一条日志。
func TestSanitizeEnvAppliesInEveryMode(t *testing.T) {
	resetMode(t)
	for _, mode := range []execpolicy.Mode{execpolicy.ModeOff, execpolicy.ModeWarn, execpolicy.ModeEnforce} {
		log := newRecordLogger()
		execpolicy.Configure(mode, log)
		out := execpolicy.SanitizeEnv([]string{"LD_PRELOAD=/tmp/x", "GIT_SSH_COMMAND=/tmp/y", "KEEP=1"})
		if len(out) != 1 || out[0] != "KEEP=1" {
			t.Errorf("mode=%s: 输出 = %v，危险变量未剥离", mode, out)
		}
		if log.warnCount() != 1 {
			t.Errorf("mode=%s: 剥离日志条数 = %d，期望 1", mode, log.warnCount())
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
