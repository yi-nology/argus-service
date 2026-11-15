// store_helpers.go —— 包级通用助手：ID 生成、JSON 列解码、PR 条件、静默查询与退避。

package storage

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// NewTaskID 生成任务 ID（service/poller 共用单一出处）。
func NewTaskID() string {
	return "t-" + strings.ToLower(strings.ReplaceAll(uuid.NewString(), "-", ""))[:16] + time.Now().Format("01021504")
}

// DecodeJSONMap 解码 Task 的 JSON 对象列（artifacts/debug/stage_artifacts）：
// 空串/坏数据安全返回空表。runner 写入与 service/MCP 读取共用单源（v3.99.0，
// 此前两端各持一份同形解码，字段契约无钉）。
func DecodeJSONMap(s string) map[string]string {
	m := map[string]string{}
	if s == "" {
		return m
	}
	_ = json.Unmarshal([]byte(s), &m)
	return m
}

// wherePR PR 四元组定位条件（storage 各表通用单源，v3.99.0：8 处同形
// Where 字符串收敛）。
func wherePR(q *gorm.DB, platform, owner, repo, number string) *gorm.DB {
	return q.Where("platform = ? AND owner = ? AND repo = ? AND number = ?", platform, owner, repo, number)
}

// quietDB 预期内"查无记录"的路径专用：GORM 默认把 ErrRecordNotFound 打成红色错误日志，
// worker 每 500ms 空轮询一次会刷屏掩盖真实告警——这些路径静默。
func (s *Store) quietDB() *gorm.DB {
	return s.db.Session(&gorm.Session{Logger: logger.Discard})
}

// retryBackoff 第 attempt 次失败的退避：base×3^(attempt-1)，封顶 1h。
func retryBackoff(base time.Duration, attempt int) time.Duration {
	d := base
	for i := 1; i < attempt && d < time.Hour; i++ {
		d *= 3
	}
	if d > time.Hour {
		d = time.Hour
	}
	return d
}
