// migrate_mode.go —— 迁移执行模式（#29 P3-04 Migration Job 拆分）：
// ARGUS_MIGRATE_MODE 三态，把「迁移」从副本启动路径拆成可独立编排的两半——
//
//	""（缺省） 启动期迁移（现状不变：schema+数据回写，租约/fail-closed 语义全保留）
//	"job"      同缺省执行迁移，但 main 侧完成后进程退出 0（k8s Job 先行形态）
//	"verify"   只校验不迁移：关键表/回写哨兵/在飞防线只读复核，缺失 fail-closed
//	           拒绝启动（副本滚动重启形态——迁移由先行 Job 完成）
//
// 未知值 fail-closed：拼写错误静默回落启动期迁移会让「只校验」的副本悄悄执行
// DDL，违背拆分意图——带病降级比启动失败更危险（与迁移租约超时同哲学）。
package storage

import (
	"context"
	"fmt"
	"os"
)

// MigrateMode 读取并校验 ARGUS_MIGRATE_MODE（""|job|verify）。
// 校验单点：storage.Open 与 main（job 退出判定）都经本函数，非法值两侧一致拒绝。
func MigrateMode() (string, error) {
	v := os.Getenv("ARGUS_MIGRATE_MODE")
	switch v {
	case "", "job", "verify":
		return v, nil
	default:
		return "", fmt.Errorf("storage: ARGUS_MIGRATE_MODE=%q 非法（有效值：job/verify，缺省=启动期迁移）", v)
	}
}

// verifySchemaReady 只读校验（ARGUS_MIGRATE_MODE=verify）：不执行任何 DDL/DML，
// 复核 Migration Job 已把库迁到位——全量关键表存在（autoMigrateModels 同源）+
// 一次性回写哨兵已登记（oneTimeMigrations 同源）+ 在飞唯一防线已建立。
// 任一缺失 fail-closed 拒绝启动：半迁移态副本服务比启动失败更危险（P0-01 同哲学）。
func (s *Store) verifySchemaReady(ctx context.Context) error { return verifySchemaReady(s, ctx) }
