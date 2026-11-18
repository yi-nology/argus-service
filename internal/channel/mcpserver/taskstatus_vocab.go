// taskstatus_vocab.go —— list_tasks 状态词表（本包具名常量组）。
//
// mcpserver 零 internal 依赖（arch 铁律），不能引 storage 常量——跨包词表
// 一致性由 taskstatus_vocab_test 钉（与 internal/storage.AllTaskStatuses()
// 逐值相等）；新增任务状态两边同改，测试当天红（word_tables 门禁的
// mcpserver 侧镜像形态，与 platformVocab 同纪律）。

package mcpserver

// 任务状态词表（值域 = internal/storage 任务状态词表；本包另起名字是
// kernel word_tables distinct 语义的宿主侧形态）。
const (
	TaskStatusPending = "pending"
	TaskStatusRunning = "running"
	TaskStatusDone    = "done"
	TaskStatusFailed  = "failed"
	TaskStatusStale   = "stale"
)

// taskStatusChoices 工具枚举展示序（词表声明序，非排序序）。
var taskStatusChoices = []string{TaskStatusPending, TaskStatusRunning, TaskStatusDone, TaskStatusFailed, TaskStatusStale}
