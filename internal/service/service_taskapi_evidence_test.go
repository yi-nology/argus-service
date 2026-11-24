package service

// GET /tasks/{id} 出参证据族（v7.3.x thrift 对称补齐）：ResultJSON findings 的
// Evidence/EvidenceStatus 经 thrift Finding 字段 6/7 透出——HTTP 面与 MCP
// findingDTO 字段族对称（Phase 2 四态出口在 HTTP 面可见）。

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/yi-nology/argus/internal/config"
	"github.com/yi-nology/argus/internal/storage"
)

// TestGetTaskExposesEvidenceFields ResultJSON → thrift Finding 的 evidence/
// evidence_status 透传。
func TestGetTaskExposesEvidenceFields(t *testing.T) {
	store := openVisStore(t) // 真 Store（sqlite 临时库），复用可见性测试辅助
	s := &Service{Cfg: &config.Config{}, Store: store}
	ctx := context.Background()

	resultJSON, _ := json.Marshal(map[string]any{
		"verdict": "comment",
		"findings": []map[string]any{
			{"file": "a.go", "line": 12, "severity": "medium", "comment": "x",
				"by": "builtin", "evidence": "a.go:12 未判空",
				"evidence_status": "structurally_valid"},
			{"file": "b.go", "line": 3, "severity": "low", "comment": "y",
				"by": "builtin", "evidence_status": "unverified"},
		},
	})
	task := storage.NewTask("code_review", "gitea", "acme", "app", "1")
	task.ID = "t-evtest"
	task.Status = storage.StatusDone
	task.ResultJSON = string(resultJSON)
	if err := store.CreateTask(ctx, task); err != nil {
		t.Fatal(err)
	}

	resp, code := s.GetTask(ctx, "t-evtest")
	if code != 200 || resp == nil || len(resp.Findings) != 2 {
		t.Fatalf("GetTask code=%d findings=%d", code, len(resp.Findings))
	}
	f := resp.Findings[0]
	if f.Evidence == nil || *f.Evidence != "a.go:12 未判空" {
		t.Fatalf("evidence 未透出: %+v", f.Evidence)
	}
	if f.EvidenceStatus == nil || *f.EvidenceStatus != "structurally_valid" {
		t.Fatalf("evidence_status 未透出: %+v", f.EvidenceStatus)
	}
	if resp.Findings[1].EvidenceStatus == nil || *resp.Findings[1].EvidenceStatus != "unverified" {
		t.Fatalf("第二条状态错: %+v", resp.Findings[1].EvidenceStatus)
	}
}

// TestGetTaskExposesReplyOutcome 回复控制结果透出（v8.5.0）：reply_state 与
// 静默条数进 GetTaskResponse；零值不产出字段（thrift optional 语义）。
func TestGetTaskExposesReplyOutcome(t *testing.T) {
	store := openVisStore(t)
	s := &Service{Cfg: &config.Config{}, Store: store}
	ctx := context.Background()

	task := storage.NewTask("code_review", "gitea", "acme", "app", "2")
	task.Status = storage.StatusDone
	task.ReplyState = storage.ReplyStateThrottled
	task.SilencedFloor = 2
	task.SilencedCap = 1
	if err := store.CreateTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	resp, code := s.GetTask(ctx, task.ID)
	if code != 200 {
		t.Fatalf("code=%d", code)
	}
	if resp.ReplyState == nil || *resp.ReplyState != storage.ReplyStateThrottled {
		t.Fatalf("reply_state 未透出: %v", resp.ReplyState)
	}
	if resp.SilencedFloor == nil || *resp.SilencedFloor != 2 || resp.SilencedCap == nil || *resp.SilencedCap != 1 {
		t.Fatalf("静默计数未透出: floor=%v cap=%v", resp.SilencedFloor, resp.SilencedCap)
	}

	// 零值：未达回帖出口/无静默 → 三个 optional 字段均不产出
	clean := storage.NewTask("code_review", "gitea", "acme", "app", "3")
	clean.Status = storage.StatusDone
	if err := store.CreateTask(ctx, clean); err != nil {
		t.Fatal(err)
	}
	resp, _ = s.GetTask(ctx, clean.ID)
	if resp.ReplyState != nil || resp.SilencedFloor != nil || resp.SilencedCap != nil {
		t.Fatalf("零值不应产出字段: state=%v floor=%v cap=%v",
			resp.ReplyState, resp.SilencedFloor, resp.SilencedCap)
	}
}
