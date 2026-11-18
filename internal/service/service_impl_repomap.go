// service_impl_repomap.go —— Service 方法体的包级实现（转发壳见原文件）。

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/yi-nology/argus/internal/execx"
	"github.com/yi-nology/argus/internal/tenant"
	"github.com/yi-nology/argus/internal/impactview"
)

// repoMapShareVerifier 的包级实现（转发壳见原文件）。#91：坐标反查租户 +
// 访问审计（share 面此前绕过一切门且零审计）。
func repoMapShareVerifier(s *Service) func(*app.RequestContext) bool {
	secret := s.Cfg.APIToken
	return func(c *app.RequestContext) bool {
		platform, owner, repo := c.Query("platform"), c.Query("owner"), c.Query("repo")
		tid := tenant.Default
		if s.Store != nil {
			if t, ok := s.Store.ResolveTenantRepo(context.Background(), platform, owner, repo); ok {
				tid = t
			}
		}
		if !impactview.ImpactShareValid(secret, tid, repoMapShareID(platform, owner, repo), c.Query("share")) {
			return false
		}
		s.Audit.Log("repomap.share_access", "repo", platform+":"+owner+"/"+repo, "tenant", tid)
		return true
	}
}

// repoMapPlatform 的包级实现（转发壳见原文件）。
func repoMapPlatform(s *Service, explicit string) (string, error) {
	if explicit != "" {
		if _, ok := s.Cfg.Platforms[explicit]; !ok {
			return "", fmt.Errorf("未知平台 %q", explicit)
		}
		return explicit, nil
	}
	set := make([]string, 0, 2)
	for p, pc := range s.Cfg.Platforms {
		if pc.Token != "" {
			set = append(set, p)
		}
	}
	switch len(set) {
	case 1:
		return set[0], nil
	case 0:
		return "", fmt.Errorf("未配置任何平台凭据")
	default:
		return "", fmt.Errorf("多平台已配置（%s），请以 ?platform= 指定", strings.Join(set, "/"))
	}
}

// repoMapView 的包级实现（转发壳见原文件）。
func repoMapView(s *Service, ctx context.Context, platform, owner, repo string) (string, bool) {
	if !s.Authorize(ctx, platform, owner, repo) {
		authzDeniedMetric(ctx, "/repos/map")
		return ErrAuthzDenied.Error(), false
	}
	b, err := s.repoMap(ctx, platform, owner, repo)
	if err != nil {
		return err.Error(), false
	}
	return string(b), true
}

// repoMapHTML 的包级实现（转发壳见原文件）。
func repoMapHTML(s *Service, ctx context.Context, platform, owner, repo string) (string, error) {
	if !s.Authorize(ctx, platform, owner, repo) {
		authzDeniedMetric(ctx, "/repos/map")
		return "", ErrAuthzDenied
	}
	b, err := s.repoMap(ctx, platform, owner, repo)
	if err != nil {
		return "", err
	}
	var snap impactview.Snapshot
	if err := json.Unmarshal(b, &snap); err != nil {
		return "", fmt.Errorf("地图快照损坏: %w", err)
	}
	htmlOut := impactview.RenderRepoHTML(&snap, owner+"/"+repo+"（"+platform+"）")
	if htmlOut == "" {
		return "", fmt.Errorf("无可视化节点（空图）")
	}
	return htmlOut, nil
}

// repoMap 的包级实现（转发壳见原文件）。
func repoMap(s *Service, ctx context.Context, platform, owner, repo string) ([]byte, error) {
	if !repoMapNameChars.MatchString(owner) || !repoMapNameChars.MatchString(repo) ||
		strings.Contains(owner, "..") || strings.Contains(repo, "..") {
		return nil, fmt.Errorf("owner/repo 含非法字符")
	}
	key := platform + ":" + owner + "/" + repo
	now := time.Now()

	// 阶段一：锁内窥探缓存态（不阻塞）。
	repoMaps.mu.Lock()
	e, hit := repoMaps.items[key]
	probeDir := ""
	probeSnap := []byte(nil)
	probedAt := time.Time{}
	if hit && e.snap != nil && now.Sub(e.at) < repoMapTTL {
		probeDir, probeSnap, probedAt = e.dir, e.snap, e.at
	}
	repoMaps.mu.Unlock()

	// 阶段二（锁外）：freshness 探测。任何失败按失配处理——fail-safe 刷新
	//（v6.7.0 P1-04：此前 ls-remote 失败按「一致」处理，HEAD 对比恒 true，
	// 提交更新后代码地图永不刷新）。
	// v6.12 P1（外部评估实锤）：探测失配不自删 probeDir——并发请求 A 可能
	// 正处在阶段三重建扫描中，B 删其工作目录即地图损坏。代际标记（staleDir
	// 保留给 in-flight 重建方在成功落缓存后统一回收）+ 阶段三 fresh 分支
	// 「同代同 dir」命中即返回缓存——多探测者最多一个进入重建（退位语义），
	// 不再互删。
	probedStale := false
	if probeDir != "" {
		if s.repoMapHeadMatches(ctx, probeDir, platform) {
			return probeSnap, nil
		}
		probedStale = true // 不自删——代际标记留给重建方回收
	}

	// 阶段三：锁内复查 + in-flight 占位（check-and-set 原子，防并发双克隆）。
	// 探测期间他请求可能已重建完成（e.at 推进）——TTL 内直接采信；但自己刚
	// 探测失配的同一代快照不得再采信（否则刷新被旧缓存短路，永不生效）。
	// v6.12 P1：同代同 dir 不采信（e.at.Equal(probedAt) 判同代，不论 dir 是否
	// 变化）——并发重建方落缓存时必推进 e.at（at: time.Now()），第二个探测者
	// 进入本阶段看到 e.at 已推进即知重建完成、采信新代。旧代同 dir 残留仅
	// 出现在「自己即将重建」的占位被回收前，不得短路。
	repoMaps.mu.Lock()
	if e, ok := repoMaps.items[key]; ok {
		fresh := now.Sub(e.at) < repoMapTTL
		probedSelf := probedStale && e.at.Equal(probedAt)
		switch {
		case e.snap != nil && fresh && !probedSelf:
			repoMaps.mu.Unlock()
			return e.snap, nil
		case e.snap == nil && e.errMsg == "" && fresh:
			repoMaps.mu.Unlock()
			return nil, fmt.Errorf("地图生成中（浅克隆+扫描），数十秒后重试")
		case e.errMsg != "" && now.Sub(e.at) < repoMapTTL/2:
			repoMaps.mu.Unlock()
			return nil, fmt.Errorf("%s", e.errMsg)
		}
	}
	repoMaps.items[key] = repoMapEntry{at: now} // in-flight 占位
	repoMaps.mu.Unlock()

	var snap *impactview.Snapshot
	// v6.12 P1：重建方克隆前清理自己探测失配的那一代目录——cloneForMap 对
	// 同哈希目录已有 .git 直接复用（旧 HEAD 仍在），不清理则「重建」实为空转。
	// 安全性：只有重建方（in-flight 占位持有者）删自己刚探测过的目录；并发
	// 探测者已进入阶段三会命中占位返回「生成中」，不会走到这里；若另一重建
	// 方抢先占位，本请求已在上方 fresh 分支采信/报错返回，亦不会走到这里。
	if probedStale && probeDir != "" {
		_ = os.RemoveAll(probeDir)
	}
	dir, err := s.cloneForMap(ctx, platform, owner, repo)
	if err == nil {
		snap = impactview.BuildRepoMap(dir, repoMapScanTimeout)
	}
	b, _ := json.Marshal(snap)
	repoMaps.mu.Lock()
	if err != nil {
		// 失败短因缓存半程 TTL（防坏仓反复打克隆）；in-flight 占位覆盖
		repoMaps.items[key] = repoMapEntry{at: time.Now(), errMsg: err.Error()}
		repoMaps.mu.Unlock()
		return nil, err
	}
	if len(snap.Nodes) == 0 {
		repoMaps.items[key] = repoMapEntry{at: time.Now(), empty: true, snap: b, dir: dir}
		repoMaps.mu.Unlock()
		return b, nil
	}
	repoMaps.items[key] = repoMapEntry{at: time.Now(), snap: b, dir: dir}
	repoMaps.mu.Unlock()
	return b, nil
}

// repoMapHeadMatches 的包级实现（转发壳见原文件）。
func repoMapHeadMatches(s *Service, ctx context.Context, dir, platform string) bool {
	if dir == "" {
		return false
	}
	var token string
	if s.Cfg != nil {
		if pc, ok := s.Cfg.Platforms[platform]; ok {
			token = pc.Token
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	// execx 统一出口：git 自带 -C dir，故不设 cmd.Dir（相对 dir 会被 -C 二次解析）。
	local, _, err := execx.Run(ctx, "git", []string{"-C", dir, "rev-parse", "HEAD"})
	if err != nil {
		return false
	}
	args := append([]string{"-C", dir, "ls-remote"}, repomapGitAuthArgs(token)...)
	remote, _, err := execx.Run(ctx, "git", append(args, "origin", "HEAD"))
	if err != nil {
		return false
	}
	fields := strings.Fields(string(remote))
	return len(fields) > 0 && fields[0] == strings.TrimSpace(string(local))
}

// cloneForMap 的包级实现（转发壳见原文件）。
func cloneForMap(s *Service, ctx context.Context, platform, owner, repo string) (string, error) {
	pc, ok := s.Cfg.Platforms[platform]
	if !ok || pc.Token == "" {
		return "", fmt.Errorf("平台 %s 缺少 clone 凭据", platform)
	}
	dst := repoMapWorkDir(s.Cfg.WorkRoot, platform, owner, repo)
	if fi, err := os.Stat(filepath.Join(dst, ".git")); err == nil && fi.IsDir() {
		return dst, nil // 已有副本：TTL 由快照缓存闸
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", err
	}
	s.pruneRepomapClones(dst) // 未命中路径顺手 GC（热仓走 .git 命中路径不触发，无清理churn）
	_ = os.RemoveAll(dst)     // 半截目录清理（无 .git 的残留）
	url := repomapCloneURLFn(pc.BaseURL, owner, repo)
	args := append(repomapGitAuthArgs(pc.Token), "clone", "--depth", "1", "--single-branch", url, dst)
	// 子进程环境走白名单（procx.ChildEnv 基底），不再把 os.Environ() 全量
	// 交给 git——宿主的模型 key/令牌不进外部 CLI 环境（#37 egress 同一纪律）。
	// 凭据走 repomapGitAuthArgs 的 -c http.extraHeader，不依赖环境变量。
	env := execx.ChildEnv(nil)
	if pc.SkipTLS {
		env = append(env, "GIT_SSL_NO_VERIFY=1")
	}
	_, stderr, err := execx.Run(ctx, "git", args, execx.WithEnv(env))
	if err != nil {
		msg := strings.TrimSpace(string(stderr))
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("浅克隆失败（平台 %s）: %s", platform, msg)
	}
	return dst, nil
}

// pruneRepomapClones 的包级实现（转发壳见原文件）。
func pruneRepomapClones(s *Service, keep string) {
	root := filepath.Join(s.Cfg.WorkRoot, "repomap")
	entries, err := os.ReadDir(root)
	if err != nil {
		return // 根目录不存在 = 无可清理
	}
	now := time.Now()
	for _, en := range entries {
		if !en.IsDir() || en.Name() == filepath.Base(keep) {
			continue
		}
		path := filepath.Join(root, en.Name())
		if !repoMapWorkDirName.MatchString(en.Name()) {
			_ = os.RemoveAll(path) // 旧寻址残留（含 token 落盘形态）：立即清理
			continue
		}
		if fi, ferr := en.Info(); ferr == nil && now.Sub(fi.ModTime()) > repoMapCloneGCTTL {
			_ = os.RemoveAll(path) // 孤儿克隆（仓库已删/改名）：按保留期清理
		}
	}
}
