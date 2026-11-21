// merge_op.go —— 受控合并操作（merge_operations）模型与读写面。
//
// 一次 preview 建一行 pending 候选，绑定 expected_remote_head；confirm 经
// 副作用闸 at-most-once 执行 MergeCR，终态 merged/failed/uncertain/expired。
// uncertain 由 effect 对账或 POST /merges/reconcile 按平台实际状态定案。
package storage

import (
	"context"
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"
)

// MergeOp status 词表（只有 merged/failed/expired 是稳定终态；uncertain 等对账）。
const (
	MergeOpPending   = "pending"
	MergeOpMerged    = "merged"
	MergeOpFailed    = "failed"
	MergeOpUncertain = "uncertain"
	MergeOpExpired   = "expired"
	MergeOpRejected  = "rejected" // confirm 时资格门/绑定校验失败（未发起远端写）
)

// MergeOp merge_operations 一行：preview 候选 → confirm 执行 → 终态。
type MergeOp struct {
	TenantID string `gorm:"column:tenant_id;size:64;not null;default:default;index"`
	ID       string `gorm:"column:id;primaryKey;size:40"`

	Platform string `gorm:"column:platform;size:16;not null;index:idx_merge_pr,priority:1"`
	Owner    string `gorm:"column:owner;size:128;not null;index:idx_merge_pr,priority:2"`
	Repo     string `gorm:"column:repo;size:128;not null;index:idx_merge_pr,priority:3"`
	Number   string `gorm:"column:number;size:16;not null;index:idx_merge_pr,priority:4"`

	// ExpectedRemoteHead preview 时刻绑定的远端 head；confirm 必须与请求及
	// 远端当前 head 三方一致，否则拒绝（陈旧证据不可放行）。
	ExpectedRemoteHead string `gorm:"column:expected_remote_head;size:64;not null"`

	Status string `gorm:"column:status;size:16;not null;default:pending;index"`
	// GateJSON 资格门快照（MergeEligibility 序列化；审计「当时为何放行」）。
	GateJSON   string `gorm:"column:gate_json;type:text"`
	EffectKey  string `gorm:"column:effect_key;size:255;uniqueIndex"`
	ResultJSON string `gorm:"column:result_json;type:text"`
	Reason     string `gorm:"column:reason;type:text"`
	// ConfirmedByFP confirm 调用方 token 指纹（非原 token）。
	ConfirmedByFP string `gorm:"column:confirmed_by_fp;size:32"`

	CreatedAt   time.Time  `gorm:"column:created_at;index"`
	UpdatedAt   time.Time  `gorm:"column:updated_at"`
	ConfirmedAt *time.Time `gorm:"column:confirmed_at"`
	FinishedAt  *time.Time `gorm:"column:finished_at"`
}

func (MergeOp) TableName() string { return "merge_operations" }

// NewMergeOpID 生成合并操作 ID（与 NewTaskID 同风格，前缀区分域）。
func NewMergeOpID() string {
	return "m-" + NewTaskID()[2:] // reuse entropy; strip t- prefix
}

// CreateMergeOp 落一行 pending 候选（effect_key 形态 merge:<op_id>）。
func (s *Store) CreateMergeOp(ctx context.Context, op *MergeOp) error {
	return s.withWrite(func() error {
		if op.ID == "" {
			op.ID = NewMergeOpID()
		}
		if op.EffectKey == "" {
			op.EffectKey = "merge:" + op.ID
		}
		if op.Status == "" {
			op.Status = MergeOpPending
		}
		now := time.Now()
		op.CreatedAt, op.UpdatedAt = now, now
		return s.db.WithContext(ctx).Create(op).Error
	})
}

// GetMergeOp 按 ID 读取（含租户谓词）。
func (s *Store) GetMergeOp(ctx context.Context, id string) (*MergeOp, error) {
	var op MergeOp
	err := s.db.WithContext(ctx).Where("id = ?", id).First(&op).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &op, nil
}

// ListPendingMergeOpsByPR 列出某 PR 的 pending 候选（最新在前）。
func (s *Store) ListPendingMergeOpsByPR(ctx context.Context, platform, owner, repo, number string) ([]MergeOp, error) {
	var out []MergeOp
	err := wherePR(s.db.WithContext(ctx), platform, owner, repo, number).
		Where("status = ?", MergeOpPending).
		Order("created_at DESC").Find(&out).Error
	return out, err
}

// TransitionMergeOp 原子状态迁移：仅允许 from→to 且当前状态仍是 from
// （CAS；并发 confirm 败方零覆写）。终态不再迁出。
func (s *Store) TransitionMergeOp(ctx context.Context, id, from, to, reason, resultJSON string) (bool, error) {
	ok := false
	err := s.withWrite(func() error {
		now := time.Now()
		q := s.db.WithContext(ctx).Model(&MergeOp{}).
			Where("id = ? AND status = ?", id, from).
			Updates(map[string]any{
				"status":     to,
				"reason":     reason,
				"updated_at": now,
			})
		if q.Error != nil {
			return q.Error
		}
		if resultJSON != "" {
			if err := s.db.WithContext(ctx).Model(&MergeOp{}).
				Where("id = ?", id).Update("result_json", resultJSON).Error; err != nil {
				return err
			}
		}
		if to == MergeOpMerged || to == MergeOpFailed || to == MergeOpExpired || to == MergeOpRejected {
			if err := s.db.WithContext(ctx).Model(&MergeOp{}).
				Where("id = ?", id).Update("finished_at", now).Error; err != nil {
				return err
			}
		}
		if to != MergeOpPending && from == MergeOpPending {
			// confirmed_at 首次离开 pending 时打点（confirm 受理时刻）。
			_ = s.db.WithContext(ctx).Model(&MergeOp{}).
				Where("id = ? AND confirmed_at IS NULL", id).Update("confirmed_at", now).Error
		}
		ok = q.RowsAffected > 0
		return nil
	})
	return ok, err
}

// CountBlockingFindings 未处理 critical/high 意见数（status=open；severity
// 归一小写比较）。lastTaskIDs 非空时仅统计 last_task_id 命中集合的行——
// 合并门用当前 head 审查任务集合收窄，避免旧 head 高危意见误阻新 head。
func (s *Store) CountBlockingFindings(ctx context.Context, platform, owner, repo, number string, lastTaskIDs []string) (int64, error) {
	q := wherePR(s.db.WithContext(ctx), platform, owner, repo, number).
		Where("status = ?", PrintOpen).
		Where("LOWER(severity) IN ?", []string{"critical", "high"})
	if len(lastTaskIDs) > 0 {
		q = q.Where("last_task_id IN ?", lastTaskIDs)
	}
	var n int64
	err := q.Model(&FindingPrint{}).Count(&n).Error
	return n, err
}

// LatestReviewTaskForHead 当前 head 上最近一次完成的审查任务（kind 审查类、
// status=done、head_sha 匹配、verdict 非空）。无则返回 (nil, nil)。
func (s *Store) LatestReviewTaskForHead(ctx context.Context, platform, owner, repo, number, headSHA string) (*Task, error) {
	if headSHA == "" {
		return nil, nil
	}
	kinds := reviewKindList()
	var t Task
	err := wherePR(s.db.WithContext(ctx), platform, owner, repo, number).
		Where("head_sha = ? AND status = ? AND verdict <> '' AND kind IN ?", headSHA, StatusDone, kinds).
		Order("finished_at DESC, created_at DESC").
		First(&t).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &t, nil
}

// ReviewTaskIDsForHead 当前 head 全部审查任务 ID（含未完成；findings 收窄用）。
func (s *Store) ReviewTaskIDsForHead(ctx context.Context, platform, owner, repo, number, headSHA string) ([]string, error) {
	if headSHA == "" {
		return nil, nil
	}
	kinds := reviewKindList()
	var ids []string
	err := s.db.WithContext(ctx).Model(&Task{}).
		Where("platform = ? AND owner = ? AND repo = ? AND number = ? AND head_sha = ? AND kind IN ?",
			platform, owner, repo, number, headSHA, kinds).
		Pluck("id", &ids).Error
	return ids, err
}

// reviewKindList 审查类 kind 清单（domain.IsReviewKind 单源反查——storage
// 不 import domain 造成环时用显式清单；与 domain.taskKinds review=true 对齐）。
func reviewKindList() []string {
	return []string{"requirement_compliance", "code_review", "visual_check"}
}

// ExpireStalePendingMergeOps 把超龄 pending 候选标 expired（confirm 前远端
// head 已漂移时由调用方在门内处理；本函数做周期清理兜底）。
func (s *Store) ExpireStalePendingMergeOps(ctx context.Context, olderThan time.Duration) (int64, error) {
	var n int64
	err := s.withWrite(func() error {
		cutoff := time.Now().Add(-olderThan)
		q := s.db.WithContext(ctx).Model(&MergeOp{}).
			Where("status = ? AND created_at < ?", MergeOpPending, cutoff).
			Updates(map[string]any{"status": MergeOpExpired, "reason": "preview expired", "updated_at": time.Now()})
		n = q.RowsAffected
		return q.Error
	})
	return n, err
}

// MergeOpEffectKey 由 op ID 构造副作用幂等键（单源，effect resolver 共用）。
func MergeOpEffectKey(opID string) string {
	return "merge:" + strings.TrimSpace(opID)
}
