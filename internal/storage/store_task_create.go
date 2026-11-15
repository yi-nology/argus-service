// store_task_create.go —— 任务创建：工厂、webhook 同 SHA 去重、TaskAPI 幂等直建。

package storage

import (
	"context"
	"errors"
	"github.com/oekit/data/gormx"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"time"

	"github.com/yi-nology/argus/internal/domain"
)

// CreateTask 新建任务（CLI/TaskAPI 提交；无幂等键）。
func (s *Store) CreateTask(ctx context.Context, t *Task) error {
	return s.withWrite(func() error {
		return s.db.WithContext(ctx).Create(t).Error
	})
}

// NewTask 建任务工厂（v3.109.0）：ID/Status/CreatedAt 三个不变量此前在
// taskapi/webhook×2/poller 四处字面量各自组装，漏一处即幻影 ID/缺时间戳的
// 形态漂移且无从测红。调用方补 kind 专属字段（HeadSHA/EventType/Priority/
// EventID/ArtifactsJSON）。
func NewTask(kind, platform, owner, repo, number string) *Task {
	return &Task{
		ID:        NewTaskID(),
		Kind:      kind,
		Platform:  platform,
		Owner:     owner,
		Repo:      repo,
		Number:    number,
		Status:    StatusPending,
		CreatedAt: time.Now(),
	}
}

// 任务去重原因词表（CreateTaskForWebhook 返回值；webhook 侧按此分发——
// 此前生产与比较两侧共 8 处裸字面量）。
const (
	DupLastHeadSHA = "last_head_sha"
	DupEventID     = "event_id"
	DupInFlight    = "in_flight"
)

// CreateTaskForWebhook 同步完成"同 SHA 去重检查 + 落库"（同一事务，§5.1 规则 4 的 TOCTOU 防线）。
// 返回 dupReason 非空表示命中去重（Dup* 词表），existingID 给出可指向的任务。
func (s *Store) CreateTaskForWebhook(ctx context.Context, t *Task) (dupReason, existingID string, err error) {
	err = s.withWrite(func() error {
		txErr := gormx.InTransactionContext(ctx, s.db, func(tx *gorm.DB) error {
			// 规则 4a：review_targets.last_head_sha == 本次 head。
			// MySQL 下对 target 行 FOR UPDATE：同 PR 的并发建任务事务在此串行化，
			// 封多副本租约过期窗口的 TOCTOU（深挖 P0；SQLite 单写串行无需锁）。
			// 首审 PR 的 target 行尚不存在、无锚可锁——残余竞窗仅影响 PR 首审，可接受。
			var target ReviewTarget
			targetQ := wherePR(tx, t.Platform, t.Owner, t.Repo, t.Number)
			if !s.isSQLite() {
				targetQ = targetQ.Clauses(clause.Locking{Strength: "UPDATE"})
			}
			err := targetQ.First(&target).Error
			if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
				return err // 瞬时 DB 错误不能静默跳过去重检查
			}
			// 同 SHA 去重只对审查类任务生效（domain.IsReviewKind：写作面/ci_analysis
			// 不写也不继承 LastHeadSHA，误挡会丢 CI 归因任务）。
			if err == nil && domain.IsReviewKind(t.Kind) && target.LastHeadSHA != "" && target.LastHeadSHA == t.HeadSHA {
				dupReason = DupLastHeadSHA
				return nil
			}
			// 规则 4b：存在同 head_sha 的 pending/running 任务（防 in-flight 竞态）
			var cnt int64
			err = tx.Model(&Task{}).
				Where("platform = ? AND owner = ? AND repo = ? AND number = ? AND kind = ? AND head_sha = ? AND status IN ?",
					t.Platform, t.Owner, t.Repo, t.Number, t.Kind, t.HeadSHA, []string{StatusPending, StatusRunning}).
				Count(&cnt).Error
			if err != nil {
				return err
			}
			if cnt > 0 {
				dupReason = DupInFlight
				var existing Task
				// 回查失败仅元数据降级（dupReason 已定，existingID 空）——留痕
				// 防静默（runner_lifecycle best-effort 写失败留痕先例，P2-9）。
				if recErr := tx.Select("id").Where(
					"platform = ? AND owner = ? AND repo = ? AND number = ? AND kind = ? AND head_sha = ? AND status IN ?",
					t.Platform, t.Owner, t.Repo, t.Number, t.Kind, t.HeadSHA,
					[]string{StatusPending, StatusRunning}).First(&existing).Error; recErr != nil {
					s.db.Logger.Warn(ctx, "argus.storage.dup_inflight_lookup_failed platform=%s owner=%s repo=%s number=%s err=%v",
						t.Platform, t.Owner, t.Repo, t.Number, recErr)
				}
				existingID = existing.ID
				return nil
			}
			return tx.Create(t).Error
		})
		if txErr != nil {
			// event_id 唯一冲突（同 delivery 并发重发：幂等预查与落库之间存在窗口）——
			// 回查既有任务按幂等重复处理，避免以 500 暴露（GitHub 对连续 500 会禁用 webhook）。
			if t.EventID != nil {
				if exist, e := s.GetTaskByEventID(ctx, *t.EventID); e == nil && exist != nil {
					dupReason, existingID = DupEventID, exist.ID
					return nil
				}
			}
			// 在飞同 SHA 唯一防线冲突（跨实例并发建同 SHA 任务）：与 in_flight 同语义
			// 回查；查不到在飞再回退最近历史任务（last_head_sha 语义）。
			// v6.8.0（P1-02，#7）：回查实参补 t.Kind——此前占位符 7 个实参 6 个，
			// 回查必失败上抛原始 txErr → webhook 回 500（「避免以 500 暴露」防线被
			// 本 bug 打穿：GitHub 对连续 500 禁用 webhook）。
			if isDuplicateKeyErr(txErr) {
				var existing Task
				if e := s.quietDB().WithContext(ctx).Where(
					"platform = ? AND owner = ? AND repo = ? AND number = ? AND kind = ? AND head_sha = ? AND status IN ?",
					t.Platform, t.Owner, t.Repo, t.Number, t.Kind, t.HeadSHA,
					[]string{StatusPending, StatusRunning}).Order("created_at DESC").First(&existing).Error; e == nil {
					dupReason, existingID = DupInFlight, existing.ID
					return nil
				}
				if e := s.quietDB().WithContext(ctx).Where(
					"platform = ? AND owner = ? AND repo = ? AND number = ? AND head_sha = ?",
					t.Platform, t.Owner, t.Repo, t.Number, t.HeadSHA).Order("created_at DESC").First(&existing).Error; e == nil {
					dupReason, existingID = DupLastHeadSHA, existing.ID
					return nil
				}
			}
			return txErr
		}
		return nil
	})
	return dupReason, existingID, err
}

// CreateTaskDirectIfIdle TaskAPI 直接触发路径的幂等建任务：同 platform/owner/repo/number
// 已存在 pending/running 任务时不新建，返回既有任务（created=false）。
// 与 CreateTaskForWebhook 的 in_flight 去重语义对齐，防手工触发与轮询/webhook 竞态双审。
// done 任务不拦——手工强制重审是 TaskAPI 的合法用途。
func (s *Store) CreateTaskDirectIfIdle(ctx context.Context, t *Task) (existing *Task, created bool, err error) {
	err = s.withWrite(func() error {
		txErr := s.quietDB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			// MySQL/pg 下先对 review_targets 行 FOR UPDATE：与 CreateTaskForWebhook 同一
			// 串行化锚点——此前 count-then-insert 无锁，跨副本可双建任务双烧 LLM
			//（v3.8.0 修复）。首审无锚行时残余竞窗由下方 duplicate 回查兜底。
			if !s.isSQLite() {
				var anchor ReviewTarget
				aq := wherePR(tx, t.Platform, t.Owner, t.Repo, t.Number).
					Clauses(clause.Locking{Strength: "UPDATE"}).First(&anchor)
				if aq.Error != nil && !errors.Is(aq.Error, gorm.ErrRecordNotFound) {
					return aq.Error
				}
			}
			var cnt int64
			if err := tx.Model(&Task{}).
				Where("platform = ? AND owner = ? AND repo = ? AND number = ? AND kind = ? AND status IN ?",
					t.Platform, t.Owner, t.Repo, t.Number, t.Kind, []string{StatusPending, StatusRunning}).
				Count(&cnt).Error; err != nil {
				return err
			}
			if cnt > 0 {
				existing = &Task{}
				if err := tx.Where("platform = ? AND owner = ? AND repo = ? AND number = ? AND kind = ? AND status IN ?",
					t.Platform, t.Owner, t.Repo, t.Number, t.Kind, []string{StatusPending, StatusRunning}).
					Order("created_at DESC, id DESC").First(existing).Error; err != nil {
					return err
				}
				return nil
			}
			if err := tx.Create(t).Error; err != nil {
				return err // 撞在飞唯一约束等：回查在事务外做（见下，REPEATABLE READ 快照限制）
			}
			created = true
			return nil
		})
		if txErr != nil {
			existing, created = nil, false
			// 撞在飞唯一约束（v6.8.0 P1-03 三方言；SQLite 并发窗口撞 samesha 部分索引
			// 同型，v3.8.0 修过 500 形态）→ 事务外回查 in-flight 任务返回幂等语义。
			// ⚠️ 回查必须在事务外：MySQL REPEATABLE READ 下事务内快照取自首读，
			// 并发方在其后提交的行不可见（三方言矩阵实锤 1062 后回查落空）——
			// quietDB 独立连接读到的是最新提交态。
			if isDuplicateKeyErr(txErr) {
				existing = &Task{}
				if qerr := s.quietDB().WithContext(ctx).
					Where("platform = ? AND owner = ? AND repo = ? AND number = ? AND kind = ? AND status IN ?",
						t.Platform, t.Owner, t.Repo, t.Number, t.Kind, []string{StatusPending, StatusRunning}).
					Order("created_at DESC, id DESC").First(existing).Error; qerr == nil {
					return nil
				}
			}
		}
		return txErr
	})
	return existing, created, err
}
