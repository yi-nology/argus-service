// workflow_impl_stages.go —— Workflow 阶段进度与详情存档的包级实现。

package requirement

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/oagentkit/textx/textutil"
	"github.com/yi-nology/argus/internal/plugin"
)

// onStage 的包级实现（转发壳见原文件）。
func onStage(w *Workflow, stage string) {
	if w.OnStage != nil {
		w.OnStage(stage)
	}
	w.checkpointSave(map[string]string{plugin.ArtifactStageProgress: stage})
}

// stashDetail 的包级实现（转发壳见原文件）。
func stashDetail(w *Workflow, content string) string {
	if w.SpillDir == "" {
		return ""
	}
	if len(content) > detailMaxBytes {
		content = content[:detailMaxBytes] + "\n…（详情超 200KB 硬上限，尾部丢弃）"
	}
	w.spillMu.Lock()
	defer w.spillMu.Unlock()
	if err := os.MkdirAll(w.SpillDir, 0o755); err != nil {
		w.Log.Warn("argus.r3.spill_mkdir_failed", "error", err.Error())
		return ""
	}
	w.spillSeq++
	ref := fmt.Sprintf("d%d", w.spillSeq)
	if err := os.WriteFile(filepath.Join(w.SpillDir, ref), []byte(content), 0o600); err != nil {
		w.Log.Warn("argus.r3.spill_write_failed", "error", err.Error())
		return ""
	}
	return ref
}

// stashOverflowAt 的包级实现（转发壳见原文件）。
func stashOverflowAt(w *Workflow, full string, max int) string {
	if max <= 0 || len(full) <= max {
		return full
	}
	head, _ := textutil.TruncRunes(full, max)
	if ref := w.stashDetail(full); ref != "" {
		return head + fmt.Sprintf("\n…（截断；完整结果 %d 字节已存为详情，用 get_detail 工具 ref=%q 获取全文）", len(full), ref)
	}
	return head + fmt.Sprintf("\n…（截断；完整结果 %d 字节超存档上限，已丢弃）", len(full))
}
