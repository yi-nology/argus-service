// vocab_task_status.go —— 任务状态判定词表三件套（kernel v0.44 word_tables
// 门禁红利接线，形态与 kernel contract 同形）：backing（分类表）+ enum
// 函数（派生全集副本）+ predicate（判定出口）同包同源——别的包手抄
// "pending"/"done" 等字面量即被 harness-word-table 规则钉红；新增 Status
// 前缀常量没进表（family 反向派生）当天即红。

package storage

import "slices"

// TaskStatusClass 任务状态分类：过程态 vs 终态（在飞防线/队列调度的
// 「哪些状态算在飞」从此读这张表，不再手抄 ('pending','running') 字面量）。
type TaskStatusClass int

const (
	// TaskStatusOpen 过程态：pending/running（在飞——同坐标同 SHA 防线覆盖）。
	TaskStatusOpen TaskStatusClass = iota
	// TaskStatusTerminal 终态：done/failed/stale。
	TaskStatusTerminal
)

// taskStatusAll 任务状态判定表（backing 单源——谓词/枚举/分类都读本表）。
var taskStatusAll = map[string]TaskStatusClass{
	StatusPending: TaskStatusOpen,
	StatusRunning: TaskStatusOpen,
	StatusDone:    TaskStatusTerminal,
	StatusFailed:  TaskStatusTerminal,
	StatusStale:   TaskStatusTerminal,
}

// IsKnownTaskStatus 任务状态判定谓词（导出消费面：跨包判状态词形一律走
// 本出口，禁止手抄字面量）。
func IsKnownTaskStatus(s string) bool { _, ok := taskStatusAll[s]; return ok }

// AllTaskStatuses 返回任务状态词表全集（排序后的新切片）——宿主渲染状态
// 选择器/校验落库值不必手抄常量清单（手抄必然漂移且无人会红）。
func AllTaskStatuses() []string {
	out := make([]string, 0, len(taskStatusAll))
	for s := range taskStatusAll {
		out = append(out, s)
	}
	slices.Sort(out)
	return out
}

// InFlightTaskStatuses 在飞状态组（过程态派生）——在飞唯一防线/心跳复位
// 的「哪些状态算在飞」读这里（guard DDL 与查询条件同源出口）。
func InFlightTaskStatuses() []string {
	out := make([]string, 0, 2)
	for s, c := range taskStatusAll {
		if c == TaskStatusOpen {
			out = append(out, s)
		}
	}
	slices.Sort(out)
	return out
}
