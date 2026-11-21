// fix_approval.go —— 高危修复审批票（T3）：critical/high 意见在修复前须
// 具名批准；拒绝/陈旧 → 阻断。run 级语义：修复推送环要求本批全部高危票
// 已批准；单条 @argus fix / MCP fix_finding 要求该 fp 的票已批准。
//
// 票按 (PR, fp, head) 一行：新 head 上重新生成，旧 head 的 pending 标 stale
// （陈旧证据不可放行）。medium/low 不建票（不阻断修复）。
package storage

import (
	"context"
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"
)

// FixApproval status 词表。
const (
	FixApprovalPending  = "pending"
	FixApprovalApproved = "approved"
	FixApprovalRejected = "rejected"
	FixApprovalStale    = "stale" // head 已前进，本票作废
)

// blockingSeverity 须审批的严重级别（小写归一比较）。
var blockingSeverity = map[string]bool{"critical": true, "high": true}

// IsBlockingSeverity 是否须走审批票（critical/high）。
func IsBlockingSeverity(sev string) bool {
	return blockingSeverity[strings.ToLower(strings.TrimSpace(sev))]
}

// FixApprovalTicket 一行审批票。
type FixApprovalTicket struct {
	TenantID string `gorm:"column:tenant_id;size:64;not null;default:default;uniqueIndex:idx_fixap_fp,priority:1"`
	ID       string `gorm:"column:id;primaryKey;size:40"`

	Platform string `gorm:"column:platform;size:16;not null;uniqueIndex:idx_fixap_fp,priority:2;index:idx_fixap_pr,priority:1"`
	Owner    string `gorm:"column:owner;size:128;not null;uniqueIndex:idx_fixap_fp,priority:3;index:idx_fixap_pr,priority:2"`
	Repo     string `gorm:"column:repo;size:128;not null;uniqueIndex:idx_fixap_fp,priority:4;index:idx_fixap_pr,priority:3"`
	Number   string `gorm:"column:number;size:16;not null;uniqueIndex:idx_fixap_fp,priority:5;index:idx_fixap_pr,priority:4"`

	TaskID  string `gorm:"column:task_id;size:40"`
	HeadSHA string `gorm:"column:head_sha;size:64;not null;uniqueIndex:idx_fixap_fp,priority:6;index:idx_fixap_head,priority:1"`
	Print   string `gorm:"column:print;size:80;not null;uniqueIndex:idx_fixap_fp,priority:7"`

	File     string `gorm:"column:file;size:512"`
	Severity string `gorm:"column:severity;size:16"`
	Comment  string `gorm:"column:comment;type:text"`

	Status      string `gorm:"column:status;size:16;not null;default:pending;index:idx_fixap_head,priority:2;index"`
	DecidedByFP string `gorm:"column:decided_by_fp;size:32"`
	Reason      string `gorm:"column:reason;type:text"`
	// Round 生成轮次（= task_id；同 head 重建时沿用，防重复建票）。
	Round string `gorm:"column:round;size:40"`

	CreatedAt time.Time  `gorm:"column:created_at;index"`
	UpdatedAt time.Time  `gorm:"column:updated_at"`
	DecidedAt *time.Time `gorm:"column:decided_at"`
}

func (FixApprovalTicket) TableName() string { return "fix_approval_tickets" }

// NewFixApprovalID 票 ID（m- 前缀与 merge op 区分：用 a-）。
func NewFixApprovalID() string {
	return "a-" + NewTaskID()[2:]
}

// FixApprovalSeed 生成票的输入行（审查完成后的高危意见）。
type FixApprovalSeed struct {
	Print    string
	File     string
	Severity string
	Comment  string
}

// EnsureFixApprovals 为当前 head 的高危意见建票（幂等）：
//   - 同 (pr, fp, head) 已有票 → 跳过（保留既有裁决）；
//   - 同 PR 其它 head 的 pending → 标 stale（陈旧证据不可放行）；
//   - 返回新建 + 现存 pending 数。
func (s *Store) EnsureFixApprovals(ctx context.Context, platform, owner, repo, number, headSHA, taskID string, seeds []FixApprovalSeed) (created, pending int, err error) {
	if headSHA == "" {
		return 0, 0, nil
	}
	err = s.withWrite(func() error {
		now := time.Now()
		// 旧 head pending → stale
		if e := s.db.WithContext(ctx).Model(&FixApprovalTicket{}).
			Where("platform = ? AND owner = ? AND repo = ? AND number = ? AND head_sha <> ? AND status = ?",
				platform, owner, repo, number, headSHA, FixApprovalPending).
			Updates(map[string]any{"status": FixApprovalStale, "updated_at": now}).Error; e != nil {
			return e
		}
		for _, seed := range seeds {
			if !IsBlockingSeverity(seed.Severity) {
				continue
			}
			if strings.TrimSpace(seed.Print) == "" {
				continue
			}
			var n int64
			q := s.db.WithContext(ctx).Model(&FixApprovalTicket{}).
				Where("platform = ? AND owner = ? AND repo = ? AND number = ? AND head_sha = ? AND print = ?",
					platform, owner, repo, number, headSHA, seed.Print)
			if err := q.Count(&n).Error; err != nil {
				return err
			}
			if n > 0 {
				continue
			}
			row := FixApprovalTicket{
				ID: NewFixApprovalID(), Platform: platform, Owner: owner, Repo: repo, Number: number,
				TaskID: taskID, HeadSHA: headSHA, Print: seed.Print,
				File: seed.File, Severity: strings.ToLower(seed.Severity), Comment: seed.Comment,
				Status: FixApprovalPending, Round: taskID, CreatedAt: now, UpdatedAt: now,
			}
			if err := s.db.WithContext(ctx).Create(&row).Error; err != nil {
				return err
			}
			created++
		}
		var p int64
		if err := s.db.WithContext(ctx).Model(&FixApprovalTicket{}).
			Where("platform = ? AND owner = ? AND repo = ? AND number = ? AND head_sha = ? AND status = ?",
				platform, owner, repo, number, headSHA, FixApprovalPending).
			Count(&p).Error; err != nil {
			return err
		}
		pending = int(p)
		return nil
	})
	return created, pending, err
}

// ListFixApprovals 按 PR 列票（可选 head 过滤；最新在前）。
func (s *Store) ListFixApprovals(ctx context.Context, platform, owner, repo, number, headSHA string) ([]FixApprovalTicket, error) {
	q := wherePR(s.db.WithContext(ctx), platform, owner, repo, number)
	if headSHA != "" {
		q = q.Where("head_sha = ?", headSHA)
	}
	var out []FixApprovalTicket
	err := q.Order("created_at DESC").Find(&out).Error
	return out, err
}

// GetFixApproval 按 ID 读票。
func (s *Store) GetFixApproval(ctx context.Context, id string) (*FixApprovalTicket, error) {
	var t FixApprovalTicket
	err := s.db.WithContext(ctx).Where("id = ?", id).First(&t).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &t, nil
}

// GetFixApprovalByFP 按 (pr, fp, head) 读票；无则 (nil, nil)。
func (s *Store) GetFixApprovalByFP(ctx context.Context, platform, owner, repo, number, headSHA, print string) (*FixApprovalTicket, error) {
	var t FixApprovalTicket
	err := wherePR(s.db.WithContext(ctx), platform, owner, repo, number).
		Where("head_sha = ? AND print = ?", headSHA, print).
		First(&t).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &t, nil
}

// DecideFixApproval 裁决一张 pending 票（CAS：仅 pending 可裁决）。
// decision ∈ {approved, rejected}。返回 false = 非 pending（幂等拒绝）。
func (s *Store) DecideFixApproval(ctx context.Context, id, decision, decidedByFP, reason string) (bool, error) {
	if decision != FixApprovalApproved && decision != FixApprovalRejected {
		return false, errors.New("fixapproval: decision 须 approved|rejected")
	}
	ok := false
	err := s.withWrite(func() error {
		now := time.Now()
		q := s.db.WithContext(ctx).Model(&FixApprovalTicket{}).
			Where("id = ? AND status = ?", id, FixApprovalPending).
			Updates(map[string]any{
				"status": decision, "decided_by_fp": decidedByFP,
				"reason": reason, "decided_at": now, "updated_at": now,
			})
		if q.Error != nil {
			return q.Error
		}
		ok = q.RowsAffected > 0
		return nil
	})
	return ok, err
}

// CountPendingFixApprovals 当前 head 的 pending 票数。
func (s *Store) CountPendingFixApprovals(ctx context.Context, platform, owner, repo, number, headSHA string) (int64, error) {
	var n int64
	err := wherePR(s.db.WithContext(ctx), platform, owner, repo, number).
		Where("head_sha = ? AND status = ?", headSHA, FixApprovalPending).
		Model(&FixApprovalTicket{}).Count(&n).Error
	return n, err
}

// FixApprovalGate 判定一组高危 fp 是否全部获批。
// 缺票（未生成）按未批准处理（fail-closed）。返回 (ok, blockedFPs)。
func (s *Store) FixApprovalGate(ctx context.Context, platform, owner, repo, number, headSHA string, prints []string) (bool, []string, error) {
	if len(prints) == 0 {
		return true, nil, nil
	}
	if headSHA == "" {
		return false, prints, nil // head 未知不放行
	}
	var blocked []string
	for _, p := range prints {
		if p == "" {
			continue
		}
		t, err := s.GetFixApprovalByFP(ctx, platform, owner, repo, number, headSHA, p)
		if err != nil {
			return false, nil, err
		}
		if t == nil || t.Status != FixApprovalApproved {
			blocked = append(blocked, p)
		}
	}
	return len(blocked) == 0, blocked, nil
}
