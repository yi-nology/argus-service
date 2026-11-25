// task_stages_http_test.go —— GET /tasks/:taskId/stages 路由契约（v8.6）：
// 授权内 200（stages[]/done 动态 JSON），跨仓按不存在处理 404（不泄露存在性）。
package e2e

import (
	"net/http"
	"strings"
	"testing"
)

func TestTaskStagesHTTP(t *testing.T) {
	s := newStack(t, stackOpts{apiTokens: "tok-r:read@gitea:myorg/*"})
	mine := seedDoneTask(t, s, "gitea", "myorg", "app", "aaaaaaaaaaaaaaaaaaaa")
	theirs := seedDoneTask(t, s, "gitea", "other", "secret", "bbbbbbbbbbbbbbbbbbbb")

	code, body := authzDo(t, s, http.MethodGet, "/tasks/"+mine.ID+"/stages", "tok-r", "")
	if code != http.StatusOK {
		t.Fatalf("约束内阶段时间线 = %d body=%s", code, body)
	}
	if !strings.Contains(body, `"stage":"R1"`) || !strings.Contains(body, `"done":true`) {
		t.Fatalf("阶段时间线形态不符: %s", body)
	}

	// 跨仓任务按不存在处理（与 TaskStages 的 taskVisible 纪律一致，不泄露存在性）。
	code, _ = authzDo(t, s, http.MethodGet, "/tasks/"+theirs.ID+"/stages", "tok-r", "")
	if code != http.StatusNotFound {
		t.Fatalf("跨仓阶段时间线 = %d（期望 404 不泄露存在性）", code)
	}
}
