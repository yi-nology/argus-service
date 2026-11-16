// side_effects_release_audit.go —— 强制释放租约的持久化审计（#64）。
//
// 此前强释只有 service 层一行结构化日志——日志有保留期、不可检索、不进对账，
// 满足不了「释放操作有持久化审计」的验收。审计行与释放写在**同一事务**里：
// 释放发生则审计必在，不存在「清了租约但查无对证」的半截现场。dry-run 不落行
// （只读预估，无操作事实可记）。
package storage

import "time"

// SideEffectReleaseAudit 一次强制释放的审计行（运维/取证面，只插入不更新）。
type SideEffectReleaseAudit struct {
	ID int64 `gorm:"column:id;primaryKey;autoIncrement"`
	// Resource 释放的资源身份（branch:/pr: 形态，归属解析同 effect.ParseResourceRepo）。
	Resource string `gorm:"column:resource;size:255;not null"`
	// Holder/Fence 释放时刻的资源闸状态（holder 空 = 资源本就空闲，fence 仍是已发放的最大令牌）。
	Holder string `gorm:"column:holder;size:64"`
	Fence  int64  `gorm:"column:fence"`
	// ClearedActions 一并清掉执行意图/对账占用的 action 行数。
	ClearedActions int64 `gorm:"column:cleared_actions;not null"`
	// MarkedUncertain 其中因 Unsafe + 在飞意图被转入 uncertain 的行数（#64 协议：
	// 这部分 Key 必须先对账/人工 force_retryable 才会重新开门）。
	MarkedUncertain int64 `gorm:"column:marked_uncertain;not null"`
	// OperatorFP 操作员 token 指纹（sha256 前 8 字节 hex；不落原 token，与日志面同口径）。
	OperatorFP string `gorm:"column:operator_fp;size:16"`
	// Reason 操作理由（控制面必填项，审计语义的主体）。
	Reason string `gorm:"column:reason;type:text"`
	// CreatedAt 审计时刻（清单按时间倒序回看）。
	CreatedAt time.Time `gorm:"column:created_at;index"`
}

func (SideEffectReleaseAudit) TableName() string { return "side_effect_release_audits" }
