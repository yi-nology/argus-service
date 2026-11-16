package service

import (
	"context"

	datacache "github.com/oekit/data/cache"

	"github.com/yi-nology/go-git-platform/provider"

	"github.com/yi-nology/argus/internal/commentcmd"
)

// admitComment 评论事件准入守卫：模型在位 → 负载归一化 → 解析评论要素 →
// bot 自身评论防循环 → delivery 幂等 → 仓库存世。任一不通过返回 ok=false
// （调用方静默忽略）。delivery 幂等 v3.8.0 下沉 DB（comment_deliveries 主键
// 冲突=已处理）：纯内存 map 在多副本共库与单实例重启场景下全部失效。落库失败
// fail-open（继续处理）——回帖重复可容忍，丢单不可容忍。
func (s *Service) admitComment(ctx context.Context, ev *provider.NormalizedEvent,
	delivery string) (commentcmd.Meta, bool) {
	return admitComment(s, ctx, ev, delivery)
}

// dispatchSuppression 抑制类评论指令（restore/suppress）：免 Q&A 节流——误报
// 反馈与恢复是低频运营动作且结果回流知识库，节流窗口把它们挡掉会丢反馈。
// 返回 true 表示已消费该评论。
func (s *Service) dispatchSuppression(ctx context.Context, ev *provider.NormalizedEvent,
	meta commentcmd.Meta, question string) bool {
	return dispatchSuppression(s, ctx, ev, meta, question)
}

// dispatchWriting 写作面与修复类评论指令（describe/testgen/fix，v3.15.0/v3.85.0）：
// 显式命令免意图路由，与 Q&A 同享节流与 delivery 幂等（节流已在上一步取得）。
// 返回 true 表示已消费该评论。
func (s *Service) dispatchWriting(ctx context.Context, ev *provider.NormalizedEvent,
	question, platform, prNum, prRef, delivery string) bool {
	return dispatchWriting(s, ctx, ev, question, platform, prNum, prRef, delivery)
}

// answerComment Q&A 终态分派：意图路由关闭时单轮直答（与 v3.3 行为一致）；
// 开启时先分类再分派——review 建全量复审任务，其余一律 Q&A（Fallback 也是 Q&A）。
func (s *Service) answerComment(ctx context.Context, ev *provider.NormalizedEvent,
	question, platform, prNum, prRef, delivery string) {
	answerComment(s, ctx, ev, question, platform, prNum, prRef, delivery)
}

// deliveryIDKey 回帖出口回填投递账本的 ctx 关联键（P2-03 #14）：HandleComment
// 准入后写入，postCommentReply/postQAError 读取——避免给全部指令分派链路穿参。
type deliveryIDKey struct{}

func contextWithDeliveryID(ctx context.Context, delivery string) context.Context {
	return context.WithValue(ctx, deliveryIDKey{}, delivery)
}

func deliveryIDFromContext(ctx context.Context) string {
	s, _ := ctx.Value(deliveryIDKey{}).(string)
	return s
}

// HandleComment 处理 @argus 评论：准入守卫 → 指令分派（抑制类免节流 → 节流 →
// 写作/修复类）→ Q&A 终态。轻量路径：不建 task、不入 DB、不走 worker pool。
func (s *Service) HandleComment(ctx context.Context, ev *provider.NormalizedEvent, delivery string) {
	handleComment(s, ctx, ev, delivery)
}

// actorName 事件触发者用户名（nil 安全：事件与触发者任一缺席都返回空）。
func actorName(ev *provider.NormalizedEvent) string {
	if ev == nil || ev.Actor == nil {
		return ""
	}
	return ev.Actor.Username
}

// postCommentReply 回帖 + 留痕（Q&A 与意图路由分派的共用出口）。
// v6.9.0 P2-03（#14）：回帖终态与外部副作用（平台评论 ID）回填投递账本——
// 处理事实（delivery 行）、回帖终态、外部副作用三列分离，对账面可查。
func (s *Service) postCommentReply(ctx context.Context, platform, owner, repo, prNum, prRef,
	body, question string, answerLen int) {
	postCommentReply(s, ctx, platform, owner, repo, prNum, prRef, body, question, answerLen)
}

// markDeliveryOutcome 投递账本终态回填（nil Store 安全；best-effort，失败只告警
// ——对账面缺一条不影响回帖主流程）。
func (s *Service) markDeliveryOutcome(ctx context.Context, delivery, status string, commentID int64, ref string) {
	markDeliveryOutcome(s, ctx, delivery, status, commentID, ref)
}

// handleSuppressComment 处理 MR 评论里的误报反馈：短码解析完整指纹 → 抑制入库 →
// 知识回流 → 回帖确认。复用 SuppressFinding 服务路径（同一套幂等与回流逻辑）。
func (s *Service) handleSuppressComment(ctx context.Context, ev *provider.NormalizedEvent,
	number int, fpPrefix, commentBody string) {
	handleSuppressComment(s, ctx, ev, number, fpPrefix, commentBody)
}

// knowledgeSuffix 报告尾注（知识回流是否生效取决于后端模式）。
func (s *Service) knowledgeSuffix() string { return knowledgeSuffix(s) }

// postQAError 当 Q&A 流程失败时，向 PR 发一条错误说明（避免用户无响应）。
func (s *Service) postQAError(ctx context.Context, platform, owner, repo, number, msg string) {
	postQAError(s, ctx, platform, owner, repo, number, msg)
}

// handleRestoreComment 处理 MR 评论里的恢复指令：短码解析 → 置回 open → 回帖确认。
// 与 handleSuppressComment 互为反向；不走知识回流（恢复不是误报结论）。
// v3.131.0 起执行体委派 restoreFindingCore 单源（API/MCP 同源）。
func (s *Service) handleRestoreComment(ctx context.Context, ev *provider.NormalizedEvent,
	number int, fpPrefix string) {
	handleRestoreComment(s, ctx, ev, number, fpPrefix)
}

// qaThrottle Q&A 评论 per-PR 30s 冷却缓存（per-Service 懒建，v3.84.0 起；
// v3.82.0 的进程级单例曾破坏 e2e 测试隔离。TTL 过期 + MaxSize LRU 上限由
// 库负责；qaThrottleMu 护并发首建）。
func (s *Service) qaThrottle() *datacache.LocalCache[bool] { return qaThrottle(s) }

var qaThrottled = true
