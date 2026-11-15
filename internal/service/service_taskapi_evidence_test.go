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
