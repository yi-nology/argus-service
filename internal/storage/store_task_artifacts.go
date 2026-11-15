// store_task_artifacts.go —— 任务阶段产物/artifacts_json 的合并写入。

package storage

import (
	"context"
	"encoding/json"
)

// MergeTaskStageArtifacts 合并写入任务阶段产物 checkpoint（v3.12.0 G2 断点续审）。
// patch 键覆盖同名字段；同任务由单一 runner 认领执行，read-modify-write 在
// writeMu 下无并发竞争。坏 JSON 当空处理——checkpoint 可丢（重试退化为全量
// 重跑），不可阻塞主流程。v3.185.3：同 MergeTaskArtifacts 补 "null" 钳制
// （Unmarshal("null") 成功且置 nil map,后续赋值 panic 的同型隐患）。
func (s *Store) MergeTaskStageArtifacts(ctx context.Context, id string, patch map[string]string) error {
	return s.withWrite(func() error {
		var row Task
		if err := s.db.WithContext(ctx).Select("stage_artifacts").Where("id = ?", id).First(&row).Error; err != nil {
			return err
		}
		cur := map[string]string{}
		if row.StageArtifacts != "" {
			_ = json.Unmarshal([]byte(row.StageArtifacts), &cur) // 坏 JSON 当空处理（best-effort 读，留痕：不阻断主路）
		}
		if cur == nil {
			cur = map[string]string{}
		}
		for k, v := range patch {
			cur[k] = v
		}
		b, err := json.Marshal(cur)
		if err != nil {
			return err
		}
		return s.db.WithContext(ctx).Model(&Task{}).Where("id = ?", id).
			Update("stage_artifacts", string(b)).Error
	})
}

// MergeTaskArtifacts 合并写入任务 artifacts_json（v3.183.0）：影响面快照在
// 评审早期即产出的机器可读产物（impact_view），不等 finalize 即落库——
// MCP get_impact / 影响视图 API 在任务 running 期就能消费，任务失败也不丢。
// patch 键覆盖同名字段；并发纪律同 MergeTaskStageArtifacts。
// v3.185.3 修复：TaskAPI 建的任务 artifacts_json 为字符串 "null"（nil map
// 序列化产物），Unmarshal("null") 成功且把 cur 置 nil——原实现直接 cur[k]=v
// panic（live 实弹炸出，任务卡死 running）；同型隐患顺修 stage 版。
func (s *Store) MergeTaskArtifacts(ctx context.Context, id string, patch map[string]string) error {
	return s.withWrite(func() error {
		var row Task
		if err := s.db.WithContext(ctx).Select("artifacts_json").Where("id = ?", id).First(&row).Error; err != nil {
			return err
		}
		cur := map[string]string{}
		if row.ArtifactsJSON != "" {
			// "null"/坏 JSON 均可能把 cur 反序列化成 nil——统一在下文钳回空表
			_ = json.Unmarshal([]byte(row.ArtifactsJSON), &cur) // 坏 JSON 当空处理（best-effort 读，留痕：不阻断主路）
		}
		if cur == nil {
			cur = map[string]string{}
		}
		for k, v := range patch {
			cur[k] = v
		}
		b, err := json.Marshal(cur)
		if err != nil {
			return err
		}
		return s.db.WithContext(ctx).Model(&Task{}).Where("id = ?", id).
			Update("artifacts_json", string(b)).Error
	})
}
