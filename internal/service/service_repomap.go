// service_repomap.go —— 仓库级代码地图数据面（v5.6.2，图谱可视化 D 项）：
// 按需浅克隆工作副本 → BuildRepoMap → 进程内 TTL 缓存。MCP get_repo_map 与
// GET /repos/:owner/:repo/map 双出口共用。
//
// 克隆面纪律：owner/repo 白名单字符集校验（路径/URL 注入防线）；工作目录按
// 仓库身份三元组哈希派生（v6.7.0 P0-03：owner 内 "/" 不再拼接转义，跨仓碰撞
// 串仓封死）；凭据经 http.extraHeader 注入（v6.7.0 P0-03：token 不落
// .git/config——此前 token 注入 clone URL 会被持久化到 origin remote）；日志
// 零凭据。

package service

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
)

// repoMapTTL 快照缓存有效期：目录图对"几分钟前的 main"不敏感，30 分钟足够新。
const repoMapTTL = 30 * time.Minute

// repoMapScanTimeout 单次克隆+扫描预算。
const repoMapScanTimeout = 60 * time.Second

// repoMapCloneGCTTL 孤儿克隆目录的保留期（P1-04 旧仓治理）：仓库被删/改名后
// 旧目录不再被新身份哈希命中，靠 mtime TTL 在建图未命中路径顺手清理。
const repoMapCloneGCTTL = 7 * 24 * time.Hour

// repoMapWorkDirName 新寻址目录名（platform-<hex12>）：与旧拼接式目录名互斥，
// GC 据此识别旧寻址残留（含 token 落盘 .git/config 的历史目录）立即清理。
var repoMapWorkDirName = regexp.MustCompile(`^[a-z0-9_.-]+-[0-9a-f]{12}$`)

// repoMapNameChars owner/repo 路径安全字符集（子群 owner 允许 "/"）。
var repoMapNameChars = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]*$`)

// repomapCloneURLFn clone/ls-remote URL 派生（测试钩子：本地 file 裸库回归用；
// 生产恒 repoMapCloneURL）。
var repomapCloneURLFn = repoMapCloneURL

type repoMapEntry struct {
	at     time.Time
	snap   []byte // nil = 生成中（in-flight 占位，并发请求直接让路）
	errMsg string // 生成失败短因（缓存半程 TTL，防坏仓反复打克隆）
	empty  bool   // 空图（纯文档仓）缓存
	dir    string // 工作副本目录（freshness 探测用）
}

// repoMaps 进程内缓存（Service 单例同生命周期；包级变量与 Bearer 同模式）。
var repoMaps = struct {
	mu    sync.Mutex
	items map[string]repoMapEntry
}{items: map[string]repoMapEntry{}}

// repoMapShareID 仓库地图 share 令牌域（与任务 share 同 HMAC 机制、域串不同
// ——任务令牌开不了地图页，反之亦然）。
func repoMapShareID(platform, owner, repo string) string {
	return "repo-map:" + platform + "/" + owner + "/" + repo
}

// RepoMapShareVerifier main 装配进 routerapi.SetRepoMapShare：?share= 命中
// repo-map 域令牌免 Bearer 放行该仓地图页，否则回落 Bearer。
func (s *Service) RepoMapShareVerifier() func(*app.RequestContext) bool {
	return repoMapShareVerifier(s)
}

// RepoMapPlatform 解析地图请求的平台：显式 query 优先；缺省=唯一已配置平台，
// 多平台歧义报错（提示带 platform 参数）。
func (s *Service) RepoMapPlatform(explicit string) (string, error) {
	return repoMapPlatform(s, explicit)
}

// RepoMapView MCP 面：返回快照 JSON 串；ok=false 时首返回值携带短因文本
// （生成中/凭据缺失/克隆失败——工具输出直接透出给调用方）。
// v6.7.0 P0-02：仓库维度判定收口——此前 MCP get_repo_map 无 repoAllowedFor，
// 持任意 ci 档 token 可渲染任意仓的代码地图。
func (s *Service) RepoMapView(ctx context.Context, platform, owner, repo string) (string, bool) {
	return repoMapView(s, ctx, platform, owner, repo)
}

// RepoMapHTML HTTP 面：自包含交互 HTML；错误经 error 返回（handler 出 4xx/5xx，
// ErrAuthzDenied 映射 403）。
func (s *Service) RepoMapHTML(ctx context.Context, platform, owner, repo string) (string, error) {
	return repoMapHTML(s, ctx, platform, owner, repo)
}

// repoMap 缓存主路。in-flight 占位防并发双克隆；失败短因缓存半程 TTL。
// v6.7.0（P1-04 锁范围）：freshness 探测（git 子进程 + 网络往返，10s 上界）
// 移出全局锁——此前探测持 repoMaps.mu，一个仓库的慢 ls-remote 拖住所有仓库
// 的建图请求。
func (s *Service) repoMap(ctx context.Context, platform, owner, repo string) ([]byte, error) {
	return repoMap(s, ctx, platform, owner, repo)
}

// repoMapHeadMatches 远程默认分支 HEAD 与本地副本是否一致（ls-remote 单往返，
// 10s 超时）。失败按失配处理（fail-safe 刷新）——探测是正确性依赖不是纯优化
// （v6.7.0 P1-04 语义翻转，见 repoMap 阶段二注）。凭据经 http.extraHeader
// 注入（P0-03：origin 为净 URL，token 不落盘），`-C dir` 锚定副本目录——
// 此前缺 `-C` 在服务进程 CWD 执行，「origin」不存在恒失败。
//
// 执行面走 procx 进程组纪律（#46）：Setpgid 建组 → 超时 TERM 整组 →
// WaitDelay/宽限 SIGKILL——ls-remote 的远端 helper 孙进程持 stdout 管道时，
// 裸 CommandContext.Wait 会阻塞到超过 deadline，组纪律把这层收口兜死。
func (s *Service) repoMapHeadMatches(ctx context.Context, dir, platform string) bool {
	return repoMapHeadMatches(s, ctx, dir, platform)
}

// cloneForMap 浅克隆（复用既有副本且 TTL 内免 fetch）。ctx 贯穿 git 进程取消。
// v6.7.0 P0-03：工作目录按仓库身份三元组哈希派生（碰撞对 g/s1+r 与 g+s1-r
// 不再同目录）；未命中路径顺手清理孤儿目录（旧寻址残留 + 超保留期的失配克隆）。
//
// 执行面走 procx 进程组纪律（#46）：clone 拉起的 git-remote-* helper 孙进程
// 持 stdout/stderr 管道时，裸 CommandContext 的 Wait 在 ctx 取消后仍可能阻塞
// 到 helper 自行退出；Setpgid 组 + WaitDelay 把整树按 deadline 收口。
// 环境走 procx 白名单语义（baseEnvAllow + 显式叠加；GIT_TERMINAL_PROMPT=0
// 为 procx 缺省项，防 git 凭据缺失时挂起等输入），SkipTLS 才放行
// GIT_SSL_NO_VERIFY。
func (s *Service) cloneForMap(ctx context.Context, platform, owner, repo string) (string, error) {
	return cloneForMap(s, ctx, platform, owner, repo)
}

// pruneRepomapClones 克隆目录 GC（P0-03/P1-04 旧仓治理）：①旧寻址拼接式目录
// （新寻址是 platform-<hex12> 哈希名，形态可判）立即清理——其中含旧版 token
// 落盘 .git/config 的历史泄露面；②新寻址目录 mtime 超过保留期的（仓库被删/
// 改名后的孤儿克隆）清理。只在建图未命中路径触发；skip 当前正在用的目录。
func (s *Service) pruneRepomapClones(keep string) { pruneRepomapClones(s, keep) }

// repoMapWorkDir 仓库 → 克隆目录唯一映射（v6.7.0 P0-03）：platform|owner|repo
// 三元组 SHA-256 前 6 字节 hex 派生，owner 内 "/" 不再拼接转义——旧寻址
// platform+"-"+ReplaceAll(owner,"/","-")+"-"+repo 使 GitLab 子组 g/s1+r 与
// g+s1-r 撞同一目录（跨仓工作目录不隔离 → 代码地图串仓/缓存键串仓）。
func repoMapWorkDir(root, platform, owner, repo string) string {
	sum := sha256.Sum256([]byte(platform + "|" + owner + "|" + repo))
	return filepath.Join(root, "repomap", platform+"-"+hex.EncodeToString(sum[:6]))
}

// repomapGitAuthArgs git -c 参数：Authorization 头携带平台凭据（P0-03）。-c
// 配置只作用于本次调用不落盘——此前 token 注入 clone URL 会持久化到
// .git/config 的 origin remote（目录复用 + 凭据落盘叠加放大泄露面）。token
// 空 = 不带鉴权参数。
func repomapGitAuthArgs(token string) []string {
	if token == "" {
		return nil
	}
	b64 := base64.StdEncoding.EncodeToString([]byte("oauth2:" + token))
	return []string{"-c", "http.extraHeader=Authorization: Basic " + b64}
}

// repoMapCloneURL 与 workcopy.cloneURL 同纪律：剥 API 后缀、保留原 scheme、
// 产出净 URL（不含凭据——凭据走 repomapGitAuthArgs 的 http.extraHeader，
// P0-03）。
func repoMapCloneURL(baseURL, owner, repo string) string {
	u := strings.TrimSuffix(strings.TrimSpace(baseURL), "/")
	for _, suffix := range []string{"/api/v1", "/api/v4", "/api/v3", "/api"} {
		u = strings.TrimSuffix(u, suffix)
	}
	scheme := "https://"
	if i := strings.Index(u, "://"); i >= 0 {
		scheme = u[:i+3]
		u = u[i+3:]
	}
	return fmt.Sprintf("%s%s/%s/%s.git", scheme, u, owner, repo)
}
