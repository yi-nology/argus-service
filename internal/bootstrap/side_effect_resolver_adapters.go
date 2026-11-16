// side_effect_resolver_adapters.go —— #64 副作用对账的宿主探针装配：把平台 SDK
// 与 git CLI（execx 纪律出口）适配成 effect 的窄接口（CRFinder / PushProbe）。
// 判据语义在 effect（resolver），「怎么查下游」在这里——变化原因分离。
package bootstrap

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/yi-nology/go-git-platform/provider"

	"github.com/yi-nology/argus/internal/argusctx"
	"github.com/yi-nology/argus/internal/config"
	"github.com/yi-nology/argus/internal/execx"
	"github.com/yi-nology/argus/internal/runner"
	"github.com/yi-nology/argus/internal/service"
)

// probeCommitScanPage 提交扫描的翻页深度（direct 模式判据）：近期历史找标记
// 提交，超出深度视为「无标记」（诚实边界，见 effect.PushResolver）。
const probeCommitScanPage = 30

// probeGitTimeout 单条 git ls-remote 的执行上界（远端只读探测，30s 足够；
// 挂死探测不能拖住整轮对账）。
const probeGitTimeout = 30 * time.Second

// sdkCRFinder effect.CRFinder 的 SDK 适配：按源分支查既有 PR（State 空 =
// 不过滤——closed/merged 命中同样证明创建发生过，重试会重复开 PR）。
type sdkCRFinder struct {
	sdk *argusctx.SDKCtx
}

// FindCRBySource 查源分支命中的既有 PR（第一页即可：源分支精确匹配，命中数
// 极小；查询失败按 error 透传——判据不足，本轮不裁决）。
func (f sdkCRFinder) FindCRBySource(ctx context.Context, platform, owner, repo, sourceBranch string) (int64, string, string, bool, error) {
	crs, _, err := f.sdk.ListCRs(ctx, platform, provider.ListCROptions{
		Owner: owner, Repo: repo, SourceBranch: sourceBranch,
	})
	if err != nil {
		return 0, "", "", false, err
	}
	for _, cr := range crs {
		if cr != nil && cr.SourceBranch == sourceBranch {
			return cr.ID, cr.Number, cr.WebURL, true, nil
		}
	}
	return 0, "", "", false, nil
}

// effectPushProbe effect.PushProbe 的宿主适配：branch 模式走 git ls-remote
// （裸仓可测、不依赖平台 API 形态），direct 模式走平台提交检索。
type effectPushProbe struct {
	cfg *config.Config
	sdk *argusctx.SDKCtx
}

// BranchTipSHA git ls-remote 探测远端分支 tip：无输出 = 分支不存在（ok=false，
// 查询成功）；非零退出/启动失败 = 判据不足（err）。凭证经 runner.GitAuthEnv
// （GIT_CONFIG_* env 协议）注入，token 不进 argv。
func (p effectPushProbe) BranchTipSHA(ctx context.Context, platform, owner, repo, branch string) (string, bool, error) {
	url, ok := probeCloneURL(p.cfg, platform, owner, repo)
	if !ok {
		return "", false, fmt.Errorf("effect: 平台 %s 未配置 BaseURL，无法探测远端分支", platform)
	}
	runCtx, cancel := context.WithTimeout(ctx, probeGitTimeout)
	defer cancel()
	ref := "refs/heads/" + branch
	stdout, stderr, code, err := execx.ProcessRun(runCtx, execx.ProcessRequest{
		Argv:    []string{"git", "ls-remote", url, ref},
		Env:     runner.GitAuthEnv(p.cfg, platform),
		Timeout: probeGitTimeout,
	})
	if err != nil || code != 0 {
		return "", false, fmt.Errorf("git ls-remote 失败: code=%d stderr=%s err=%v",
			code, strings.TrimSpace(stderr), err)
	}
	// 输出行形 "<sha>\t<ref>"；目标 ref 无输出 = 分支不存在。
	fields := strings.Fields(strings.TrimSpace(stdout))
	if len(fields) == 0 {
		return "", false, nil
	}
	if len(fields) < 2 {
		return "", false, fmt.Errorf("git ls-remote 输出无法解析: %q", stdout)
	}
	return fields[0], true, nil
}

// TaskCommitOnBranch 平台提交检索（direct 模式判据）：近期提交里找任务标记的
// bot 提交（作者 argus-bot + 消息含 Task: <taskID>）。
func (p effectPushProbe) TaskCommitOnBranch(ctx context.Context, platform, owner, repo, branch, taskID string) (string, bool, error) {
	pv, err := p.sdk.Provider(platform)
	if err != nil {
		return "", false, err
	}
	commits, err := pv.ListCommits(ctx, owner, repo, provider.ListCommitsOptions{
		Branch: branch, PerPage: probeCommitScanPage,
	})
	if err != nil {
		return "", false, err
	}
	for _, c := range commits {
		if c == nil {
			continue
		}
		// 平台 CRUser 无邮箱字段：作者身份以提交消息的 bot 标记为准
		// （subject 前缀 + Task: <taskID> 双重标记，见 runner.FixCommitSubjectPrefix）。
		if strings.HasPrefix(strings.TrimSpace(c.Message), runner.FixCommitSubjectPrefix) &&
			strings.Contains(c.Message, "Task: "+taskID) {
			return c.SHA, true, nil
		}
	}
	return "", false, nil
}

// probeCloneURL 平台配置 → git URL（service.CloneURL 派生单源；平台未配置 =
// 探测不可用）。BaseURL 未显式配置的平台派生不出可信 URL → 判据不足（fail-closed，
// 不猜缺省托管域名）。
func probeCloneURL(cfg *config.Config, platform, owner, repo string) (string, bool) {
	if cfg == nil {
		return "", false
	}
	pc, ok := cfg.Platforms[platform]
	if !ok || strings.TrimSpace(pc.BaseURL) == "" {
		return "", false
	}
	return service.CloneURL(pc.BaseURL, owner, repo), true
}
