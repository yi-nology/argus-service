// env.go —— 子进程危险环境变量剥离（任何模式都生效）。
package execpolicy

import (
	"strings"
	"sync"
)

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

// envStrippedOnce 记录已告警过的危险键：进程内每个键只告警一次，
// 避免长期设置 LD_LIBRARY_PATH 之类的部署按每次子进程刷屏。
var envStrippedOnce sync.Map // key -> struct{}

// SanitizeEnv 剥离危险环境变量，保留其余 KEY=VALUE 项与原顺序。
//
// 与模式无关（任何模式都剥离）；每个危险键首次被剥离时记一条 Warn
// （进程内只记一次，列出本次新剥离的键）。无 "=" 的条目按裸键判定：
// 危险的裸键同样丢弃，其余保留。
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
		var newly []string
		for _, key := range stripped {
			if _, seen := envStrippedOnce.LoadOrStore(key, struct{}{}); !seen {
				newly = append(newly, key)
			}
		}
		if len(newly) > 0 {
			if log := loadSettings().log; log != nil {
				log.Warn("argus.execpolicy.env_stripped", "vars", strings.Join(newly, ","))
			}
		}
	}
	return out
}

// resetEnvStripLog 清空「每个键只告警一次」状态（白盒测试入口）。
func resetEnvStripLog() { envStrippedOnce.Clear() }
