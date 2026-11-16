// runner_impl_gitcmd.go —— 修复链 git 命令执行基建（#66/#62）：
// 命令执行（gitRun 家族）+ 凭证环境注入（authGitEnv，GIT_CONFIG_* 协议）+
// stdout 查询（gitOutput）。执行编排（推送/开 PR/状态）在 fixpush/fixplan。

package runner

import (
	"context"
	"encoding/base64"
	"time"

	"github.com/yi-nology/argus/internal/config"
	"github.com/yi-nology/argus/internal/execx"
)

// GitAuthEnv 平台 git 写凭证的 env 注入单源（#66 GIT_CONFIG_* 协议；#64 对账
// 探针复用同一口径——ls-remote 只读探测不必另立凭证面）。cfg/platform 未配置
// 或无 token 时返回 nil（依赖环境凭证）。runner 内部走 authGitEnv 委托到这里。
func GitAuthEnv(cfg *config.Config, platform string) []string {
	if cfg == nil {
		return nil
	}
	pc, ok := cfg.Platforms[platform]
	if !ok {
		return nil
	}
	token := pc.WriteTokenOf()
	if token == "" {
		return nil
	}
	auth := base64.StdEncoding.EncodeToString([]byte("oauth2:" + token))
	// git env-config 协议（GIT_CONFIG_COUNT/KEY_n/VALUE_n）：等价 `-c key=value`
	// 但只经环境传递——token 不出现在子进程 argv（#66 ps 暴露面清零），也不落
	// .git/config。procx Env 白名单按 KEY=VALUE 字面透传（base64 padding 的 "="
	// 在首个 "=" 之后，按字面值解析不受影响）。
	return []string{
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=http.extraheader",
		"GIT_CONFIG_VALUE_0=Authorization: Basic " + auth,
	}
}

// authGitEnv 的包级实现（转发壳见原文件）。
func authGitEnv(r *Runner, platform string) []string {
	return GitAuthEnv(r.Cfg, platform)
}

func gitRun(r *Runner, ctx context.Context, dir string, argv []string) (int, string, bool) {
	return gitRunEnv(r, ctx, dir, argv, nil)
}

// gitRunEnv 带附加环境的 git 命令执行（authGitEnv 凭证注入面）。
func gitRunEnv(r *Runner, ctx context.Context, dir string, argv, env []string) (int, string, bool) {
	runCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	_, stderr, code, err := execx.ProcessRun(runCtx, execx.ProcessRequest{
		Argv: append([]string{"git"}, argv...), Env: env, Timeout: 2 * time.Minute,
	})
	return code, stderr, err == nil && code == 0
}

// gitOutput 需要 stdout 的 git 查询（diff 清单等）；gitRun 家族只回 stderr。
func gitOutput(r *Runner, ctx context.Context, dir string, argv []string) (string, bool) {
	runCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	stdout, _, code, err := execx.ProcessRun(runCtx, execx.ProcessRequest{
		Argv: append([]string{"git"}, argv...), Timeout: 2 * time.Minute,
	})
	return stdout, err == nil && code == 0
}
