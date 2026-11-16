package execpolicy

import (
	"sync"
	"testing"

	"github.com/oekit/logx"
)

// warnCapture 记录 Warn 事件（消息 + 结构化参数）。
type warnCapture struct {
	logx.Logger
	mu     sync.Mutex
	events [][]any
}

func newWarnCapture() *warnCapture { return &warnCapture{Logger: logx.Nop()} }

func (l *warnCapture) Warn(msg string, keysAndValues ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	event := append([]any{msg}, keysAndValues...)
	l.events = append(l.events, event)
}

func (l *warnCapture) snapshot() [][]any {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([][]any, len(l.events))
	copy(out, l.events)
	return out
}

// 同一危险键只告警一次；新键才产生新日志，且日志只列本次新剥离的键。
func TestSanitizeEnvLogsOncePerKey(t *testing.T) {
	resetEnvStripLog()
	t.Cleanup(resetEnvStripLog)

	log := newWarnCapture()
	Configure(ModeWarn, log)

	SanitizeEnv([]string{"LD_PRELOAD=/tmp/x", "GIT_SSH_COMMAND=/tmp/y", "KEEP=1"})
	events := log.snapshot()
	if len(events) != 1 {
		t.Fatalf("首次剥离日志条数 = %d，期望 1", len(events))
	}
	if events[0][0] != "argus.execpolicy.env_stripped" {
		t.Fatalf("日志事件名 = %v", events[0][0])
	}
	if len(events[0]) != 3 || events[0][1] != "vars" || events[0][2] != "LD_PRELOAD,GIT_SSH_COMMAND" {
		t.Fatalf("首次日志参数 = %v", events[0])
	}

	// 同键再次剥离：不重复告警。
	SanitizeEnv([]string{"LD_PRELOAD=/tmp/x", "GIT_SSH_COMMAND=/tmp/y"})
	if got := len(log.snapshot()); got != 1 {
		t.Fatalf("重复剥离后日志条数 = %d，期望仍为 1", got)
	}

	// 新键：一条新日志，且只列新键。
	SanitizeEnv([]string{"PS4=$(x)", "LD_PRELOAD=/tmp/x"})
	events = log.snapshot()
	if len(events) != 2 {
		t.Fatalf("新键剥离后日志条数 = %d，期望 2", len(events))
	}
	if len(events[1]) != 3 || events[1][2] != "PS4" {
		t.Fatalf("新键日志参数 = %v", events[1])
	}
}
