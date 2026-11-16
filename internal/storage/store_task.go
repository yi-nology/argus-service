// store_task.go —— tasks 表生命周期：崩溃复位/心跳续期/抢占/重试与状态写回。

package storage

import (
	"context"
	"time"
)

// ResetRunningToPending 把心跳过期的 running 任务重置回 pending
// （§10.4：崩溃任务否则静默丢失）。staleAfter 取心跳间隔的 ≥3 倍：
// 只有 heartbeat_at 已死（或遗留的 NULL 心跳旧行）才重置——多副本共库时，
// 一个实例滚动重启不会把其他存活实例正在执行的任务也重置（那会双执行、双回帖）。
// #32 崩溃恢复：被复位任务的未核销预留就地释放（心跳已死 = 持有进程不会
// 再续租，不等租约过期）——逐 id 条件更新（心跳在挑选与复位之间复活的任务
// 不被复位、也不误释放预留），复位成功才释放。
func (s *Store) ResetRunningToPending(ctx context.Context, staleAfter time.Duration) (n int64, err error) {
	return resetRunningToPending(s, ctx, staleAfter)
}

// TouchRunningHeartbeats 续本实例全部 running 任务的心跳（worker 池周期调用）。
// 预留租约随心跳同步续期（#32：expires_at 与任务活性绑定）。
func (s *Store) TouchRunningHeartbeats(ctx context.Context) error {
	return touchRunningHeartbeats(s, ctx)
}

// TouchHeartbeatsFor 只续期 taskIDs 中属于本实例且在跑的任务心跳
// （oagentkit/queue worker.ScopedHeartbeater 实现：Pool 探测到本能力后按本进程
// 在飞任务集续期，替代盲续 TouchRunningHeartbeats）。
//
// 三重守卫缺一不可（oagentkit/queue#42）：runner_id 防续他实例的行；
// status='running' 防把已终态的行续回新鲜；id IN (...) 是 scoped 语义的实质
// ——不在续期集里的行（runTask 已 panic/报错/成功退出的）心跳就此冻结，
// staleAfter 后由 ResetRunningToPending 复位重跑。盲续路径下这些行被本实例
// 一直续活，复位永不命中（僵尸任务静默丢失的根因）。
func (s *Store) TouchHeartbeatsFor(ctx context.Context, taskIDs []string) error {
	return touchHeartbeatsFor(s, ctx, taskIDs)
}

// ClaimNextPending worker 原子抢占一个 pending 任务（pending→running）。
// 返回 ok=false 表示当前无任务。
// MySQL 走 FOR UPDATE SKIP LOCKED 单语句取+占（多副本无空转）；SQLite 保持
// 两步条件更新（单写模型下无竞争）。抢锁失败少量重试而非直接报"无任务"——
// 此前败者直接 ok=false，worker 睡 500ms，队列有任务也空转（深挖修复）。
func (s *Store) ClaimNextPending(ctx context.Context) (*Task, bool, error) {
	return claimNextPending(s, ctx)
}

// ResetFailedForRetry failed 任务的到期重试（v3.8.0）：failed 且 attempt < maxRetries
// 且退避到期的任务重置回 pending（attempt 保留，防循环）。maxRetries<=0 = 禁用。
// 重启不重置 failed——只有此入口（或 TaskAPI 手工重审）能复活失败任务。
func (s *Store) ResetFailedForRetry(ctx context.Context, maxRetries int, now time.Time) (n int64, err error) {
	return resetFailedForRetry(s, ctx, maxRetries, now)
}
