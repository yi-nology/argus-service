package qa

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/yi-nology/argus/internal/storage"
)

// ---------- Answer：ReAct 成功 / 降级 / 双失败 / 会话落库 ----------

// ReAct 直出成功：模型一次给出无 tool_calls 的最终答复。
func TestAnswer_ReactSuccess(t *testing.T) {
	m := &fakeModel{contents: []string{"这是回答"}}
	e, store, _ := newTestEngine(t, m)
	ans, err := e.Answer(context.Background(), "gitea", "acme", "app", "7", "acme/app#7", "怎么登录？")
	if err != nil {
		t.Fatal(err)
	}
	if ans != "这是回答" {
		t.Fatalf("answer=%q", ans)
	}
	// 会话已落库且含本轮
	sess, err := store.GetQASession(context.Background(), "gitea:acme/app#7", sessionTTL)
	if err != nil || sess == nil {
		t.Fatalf("应落会话: %v", err)
	}
	var turns []storage.QATurn
	if err := json.Unmarshal([]byte(sess.HistoryJSON), &turns); err != nil {
		t.Fatal(err)
	}
	if len(turns) != 1 || turns[0].Question != "怎么登录？" || turns[0].Answer != "这是回答" {
		t.Fatalf("历史错位: %+v", turns)
	}
}

// ReAct 空回答（含回喂重试）→ 落回 single（loadContext + 单轮 Generate）。
func TestAnswer_FallbackToSingle(t *testing.T) {
	// react 把空最终答复当失败并回喂重试一次（对齐原 WithRetry 语义）：
	// 两次空 → react 整体失败；
	// 第三次 Generate 归 single。
	m := &fakeModel{contents: []string{"", "", "单轮兜底回答"}}
	e, _, _ := newTestEngine(t, m)
	ans, err := e.Answer(context.Background(), "gitea", "acme", "app", "7", "acme/app#7", "问题")
	if err != nil {
		t.Fatal(err)
	}
	if ans != "单轮兜底回答" {
		t.Fatalf("应走 single: %q", ans)
	}
	if m.callCount() < 3 {
		t.Fatalf("应三次 Generate（react+回喂+single）: %d", m.callCount())
	}
	// single 的 user 文案应带 PR 上下文围栏
	if !strings.Contains(m.lastUserText(), "PR 上下文") {
		t.Fatalf("single 应注入 PR 上下文:\n%s", m.lastUserText())
	}
}

// loadContext 失败 → single 也失败 → Answer 上抛。
func TestAnswer_BothPathsFail(t *testing.T) {
	m := &fakeModel{}
	m.errs = []error{nil} // react 可跑（但内容空→降级）；single 的 GetCR 将失败
	e, _, fp := newTestEngine(t, m)
	// 首次 Generate 返回空 → react 降级；随后 single 因 GetCR 失败退出
	m.contents = []string{""}
	fp.crErr = fmt.Errorf("api down")
	if _, err := e.Answer(context.Background(), "gitea", "acme", "app", "7", "acme/app#7", "q"); err == nil {
		t.Fatal("双路径失败应上抛")
	}
}

// 模型确定性失败（invalid api key，不重试）→ Answer 快速上抛。
func TestAnswer_ModelNonRetryable(t *testing.T) {
	m := &fakeModel{errs: []error{fmt.Errorf("401 invalid api key")}}
	e, _, _ := newTestEngine(t, m)
	start := time.Now()
	_, err := e.Answer(context.Background(), "gitea", "acme", "app", "7", "acme/app#7", "q")
	if err == nil {
		t.Fatal("模型失败应上抛")
	}
	if time.Since(start) > 3*time.Second {
		t.Fatalf("确定性失败不应重试拖时: %v", time.Since(start))
	}
}

// 多轮会话：第二轮注入第一轮历史（verbatim 窗口）。
func TestAnswer_SessionHistoryInjected(t *testing.T) {
	m := &fakeModel{contents: []string{"第一答", "第二答"}}
	e, _, _ := newTestEngine(t, m)
	if _, err := e.Answer(context.Background(), "gitea", "acme", "app", "7", "acme/app#7", "第一问"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Answer(context.Background(), "gitea", "acme", "app", "7", "acme/app#7", "第二问"); err != nil {
		t.Fatal(err)
	}
	sys := m.lastSystemText()
	if !strings.Contains(sys, "第一问") || !strings.Contains(sys, "第一答") {
		t.Fatalf("第二轮应注入第一轮历史:\n%s", sys)
	}
	// 历史在数据围栏内
	if !strings.Contains(sys, "历史对话") {
		t.Fatalf("历史应走 DataFence:\n%s", sys)
	}
}

// 坏历史 JSON 不炸：按无历史继续（留痕日志路径）。
func TestAnswer_CorruptHistory(t *testing.T) {
	m := &fakeModel{contents: []string{"ok"}}
	e, store, _ := newTestEngine(t, m)
	if err := store.SaveQASession(context.Background(), &storage.QASession{
		SessionKey: "gitea:acme/app#7", HistoryJSON: "{not-json", Summary: "",
	}); err != nil {
		t.Fatal(err)
	}
	ans, err := e.Answer(context.Background(), "gitea", "acme", "app", "7", "acme/app#7", "q")
	if err != nil || ans != "ok" {
		t.Fatalf("坏历史应降级为无历史: ans=%q err=%v", ans, err)
	}
}

// rawTurnsCap 封顶：历史超限时保存的轮次数不膨胀。
func TestAnswer_RawTurnsCap(t *testing.T) {
	e, store, _ := newTestEngine(t, &fakeModel{contents: []string{"a"}})
	key := "gitea:acme/app#7"
	// 预置满额历史
	var hist []storage.QATurn
	for i := range rawTurnsCap {
		hist = append(hist, storage.QATurn{Question: fmt.Sprintf("q%d", i), Answer: "a", At: "t"})
	}
	hj, _ := json.Marshal(hist)
	if err := store.SaveQASession(context.Background(), &storage.QASession{
		SessionKey: key, HistoryJSON: string(hj),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Answer(context.Background(), "gitea", "acme", "app", "7", "acme/app#7", "new"); err != nil {
		t.Fatal(err)
	}
	sess, _ := store.GetQASession(context.Background(), key, sessionTTL)
	var got []storage.QATurn
	_ = json.Unmarshal([]byte(sess.HistoryJSON), &got)
	if len(got) != rawTurnsCap {
		t.Fatalf("历史应封顶 %d: %d", rawTurnsCap, len(got))
	}
	if got[len(got)-1].Question != "new" {
		t.Fatalf("最新轮应在末尾: %+v", got[len(got)-1])
	}
}
