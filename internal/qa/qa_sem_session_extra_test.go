package qa

// 语义检索/调用链/符号上下文的守卫与失败路径（底座真实装配、工作副本不可用即
// 快速失败，不触网）；预算压实的失败回落；会话落库 CAS 冲突重放。

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/oagentkit/tools/workcopy"
	"github.com/oekit/logx"

	"github.com/yi-nology/argus/internal/semsearch"
	"github.com/yi-nology/argus/internal/storage"
)

// qaStubEmbedder 词袋向量化（三词正交），供 semsearch.Manager 本地建索引。
type qaStubEmbedder struct{}

func (qaStubEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	vocab := []string{"db", "http", "parse"}
	out := make([][]float32, 0, len(texts))
	for _, txt := range texts {
		v := make([]float32, len(vocab))
		for i, w := range vocab {
			v[i] = float32(strings.Count(strings.ToLower(txt), w))
		}
		out = append(out, v)
	}
	return out, nil
}

func (qaStubEmbedder) Dim() int { return 3 }

// newSemEngine 语义检索引擎：底座就绪 + 工作副本池（无凭证 → 目录解析快速失败）。
func newSemEngine(t *testing.T) *Engine {
	t.Helper()
	e, _, _ := newTestEngine(t, &fakeModel{})
	e.SemSearch = semsearch.NewManager(qaStubEmbedder{})
	e.SemSearchOn = true
	e.WC = workcopy.NewPool(t.TempDir(), logx.Nop())
	return e
}

func TestSearchSemArgumentGuards(t *testing.T) {
	ctx := context.Background()
	e := newSemEngine(t)

	if _, err := e.SearchSem(ctx, "gitea", "acme", "app", "7", "  ", 5); err == nil ||
		!strings.Contains(err.Error(), "query 为空") {
		t.Fatalf("空 query 应报错: %v", err)
	}
	// topK 0/超上限都被钳制到合法区间，随后因工作副本不可用失败（不触检索）。
	for _, topK := range []int{0, -1, 999} {
		if _, err := e.SearchSem(ctx, "gitea", "acme", "app", "7", "认证", topK); err == nil ||
			!strings.Contains(err.Error(), "工作副本不可用") {
			t.Fatalf("topK=%d 应走到工作副本门: %v", topK, err)
		}
	}

	e.SemSearchOn = false
	if _, err := e.SearchSem(ctx, "gitea", "acme", "app", "7", "认证", 5); err == nil ||
		!strings.Contains(err.Error(), "未启用") {
		t.Fatalf("开关关闭应未启用: %v", err)
	}
}

func TestTraceArgumentGuards(t *testing.T) {
	ctx := context.Background()
	e := newSemEngine(t)

	if _, err := e.Trace(ctx, "gitea", "acme", "app", "7", " ", "B"); err == nil ||
		!strings.Contains(err.Error(), "from/to 为空") {
		t.Fatalf("空 from 应报错: %v", err)
	}
	if _, err := e.Trace(ctx, "gitea", "acme", "app", "7", "A", ""); err == nil ||
		!strings.Contains(err.Error(), "from/to 为空") {
		t.Fatalf("空 to 应报错: %v", err)
	}
	if _, err := e.Trace(ctx, "gitea", "acme", "app", "7", "A", "B"); err == nil ||
		!strings.Contains(err.Error(), "工作副本不可用") {
		t.Fatalf("工作副本不可用应报错: %v", err)
	}
}

func TestSymbolContextArgumentGuards(t *testing.T) {
	ctx := context.Background()
	e := newSemEngine(t)

	if _, err := e.SymContext(ctx, "gitea", "acme", "app", "7", "  "); err == nil ||
		!strings.Contains(err.Error(), "symbol 为空") {
		t.Fatalf("空 symbol 应报错: %v", err)
	}
	if _, err := e.SymContext(ctx, "gitea", "acme", "app", "7", "Alpha"); err == nil ||
		!strings.Contains(err.Error(), "工作副本不可用") {
		t.Fatalf("工作副本不可用应报错: %v", err)
	}
}

// TestCompactHistoryFailureFallsBack 摘要模型不可用/返回空 → ok=false（回落轮数窗口）。
func TestCompactHistoryFailureFallsBack(t *testing.T) {
	history := bigTurns(12)

	// 模型未配置（Summarize 首门）。
	if _, _, ok := (&Engine{}).compactHistory(context.Background(), "p/o/r#1", history, ""); ok {
		t.Fatal("无模型应压实失败（回落轮数窗口）")
	}

	// 模型返回空摘要（summarizeTurns 判空）。
	e := &Engine{QAModel: &fakeSummaryModel{reply: ""}}
	if _, _, ok := e.compactHistory(context.Background(), "p/o/r#1", history, ""); ok {
		t.Fatal("空摘要应压实失败")
	}
}

func TestLoadQASessionStoreErrorDegrades(t *testing.T) {
	e, store, _ := newTestEngine(t, &fakeModel{})
	_ = store.Close()
	got := e.loadQASession(context.Background(), "gitea:acme/app#7", "ref")
	if got.sess != nil || len(got.turns) != 0 || got.summary != "" {
		t.Fatalf("读失败应降级无历史: %+v", got)
	}
}

func TestPersistTurnSaveErrorDoesNotPanic(t *testing.T) {
	e, store, _ := newTestEngine(t, &fakeModel{})
	_ = store.Close()
	// store 关闭 → SaveQASessionCas 报错；只留痕不 panic。
	e.persistTurn(context.Background(), "gitea:acme/app#7", "ref", qaSessionHistory{},
		"q", "a", []storage.QATurn{{Question: "q", Answer: "a"}}, "")
}

func TestPersistTurnCASConflictReplaysLatest(t *testing.T) {
	e, store, _ := newTestEngine(t, &fakeModel{})
	ctx := context.Background()
	key := "gitea:acme/app#7"

	concurrent, _ := json.Marshal([]storage.QATurn{{Question: "q-concurrent", Answer: "a-concurrent"}})
	if err := store.SaveQASession(ctx, &storage.QASession{
		SessionKey: key, HistoryJSON: string(concurrent), Summary: "s",
	}); err != nil {
		t.Fatal(err)
	}
	// stale 视图：expect 是旧时间 → CAS 冲突 → 重读最新历史并追加本轮。
	prev := qaSessionHistory{sess: &storage.QASession{
		SessionKey: key, UpdatedAt: time.Now().Add(-time.Hour),
	}}
	e.persistTurn(ctx, key, "ref", prev, "q-new", "a-new",
		[]storage.QATurn{{Question: "q-new", Answer: "a-new"}}, "")

	sess, err := store.GetQASession(ctx, key, sessionTTL)
	if err != nil || sess == nil {
		t.Fatalf("会话应存在: %v", err)
	}
	var turns []storage.QATurn
	if err := json.Unmarshal([]byte(sess.HistoryJSON), &turns); err != nil {
		t.Fatal(err)
	}
	if len(turns) != 2 || turns[0].Question != "q-concurrent" || turns[1].Question != "q-new" {
		t.Fatalf("冲突重放应保留并发轮次+本轮: %+v", turns)
	}
}

func TestPersistTurnCASConflictMalformedReload(t *testing.T) {
	e, store, _ := newTestEngine(t, &fakeModel{})
	ctx := context.Background()
	key := "gitea:acme/app#7"

	if err := store.SaveQASession(ctx, &storage.QASession{
		SessionKey: key, HistoryJSON: "{not-json",
	}); err != nil {
		t.Fatal(err)
	}
	prev := qaSessionHistory{sess: &storage.QASession{
		SessionKey: key, UpdatedAt: time.Now().Add(-time.Hour),
	}}
	e.persistTurn(ctx, key, "ref", prev, "q-new", "a-new",
		[]storage.QATurn{{Question: "q-new", Answer: "a-new"}}, "")

	sess, err := store.GetQASession(ctx, key, sessionTTL)
	if err != nil || sess == nil {
		t.Fatal(err)
	}
	var turns []storage.QATurn
	if err := json.Unmarshal([]byte(sess.HistoryJSON), &turns); err != nil {
		t.Fatal(err)
	}
	if len(turns) != 1 || turns[0].Question != "q-new" {
		t.Fatalf("历史损坏时应以本轮为起点覆盖: %+v", turns)
	}
}
