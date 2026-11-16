// service_api_cover_test.go —— 服务门面主路径行为钉住（#36 覆盖率 kernel 60/70）：
// TaskAPI（建任务/清单/报告/抑制/用量）、webhook 过滤链与建任务、评论指令分派
// （help/status/review/recheck/抑制恢复）、写作命令模板、agent 可观测面、
// 协作门控与回执。打桩纪律：平台面 svcProvider（只实现被触达能力，触达缺失
// 能力即 panic 兜底暴露）；LLM 罐头（不联网）；Store 真 sqlite。

package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/ut"
	"github.com/oagentkit/llm"
	"github.com/oagentkit/tools/workcopy"
	"github.com/oekit/logx"
	"github.com/yi-nology/go-git-platform/provider"

	"github.com/yi-nology/argus/internal/impactview"

	argus "github.com/yi-nology/argus/biz/model/argus"
	"github.com/yi-nology/argus/internal/argusctx"
	"github.com/yi-nology/argus/internal/commentcmd"
	"github.com/yi-nology/argus/internal/config"
	"github.com/yi-nology/argus/internal/dispatch"
	"github.com/yi-nology/argus/internal/domain"
	"github.com/yi-nology/argus/internal/plugin"
	"github.com/yi-nology/argus/internal/progress"
	"github.com/yi-nology/argus/internal/storage"

	oagentprogress "github.com/oagentkit/runtime/progress"
)

// ---------- 测试替身 ----------

// svcModel 罐头 LLM（写作命令/直答路径）。
type svcModel struct{}

func (svcModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	return schema.UserMessage("## 📝 概述\n变更了登录逻辑。"), nil
}

func (svcModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return nil, context.Canceled
}

// svcFailModel 恒失败 LLM（错误回帖路径用）。
type svcFailModel struct{}

func (svcFailModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	return nil, errors.New("llm down")
}

func (svcFailModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return nil, context.Canceled
}

// svcProvider 平台打桩：评论/文件/diff/协作者/回执面。
type svcProvider struct {
	provider.Provider

	mu         sync.Mutex
	diff       *provider.MergeDiff
	files      map[string]string
	notes      []string
	collabs    []string
	reactions  int
	respondErr error
}

func (f *svcProvider) Capabilities() provider.CapabilitySet {
	return provider.DeriveCapabilities(f)
}

func (f *svcProvider) GetCR(_ context.Context, _, _, _ string) (*provider.ChangeRequest, error) {
	return &provider.ChangeRequest{Number: "42", HeadSHA: "abc123"}, nil
}

func (f *svcProvider) GetRepo(_ context.Context, _, _ string) (*provider.PlatformRepo, error) {
	return &provider.PlatformRepo{DefaultBranch: "main"}, nil
}

func (f *svcProvider) GetCRDiff(_ context.Context, _, _, _ string) (*provider.MergeDiff, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.respondErr != nil {
		return nil, f.respondErr
	}
	if f.diff == nil {
		f.diff = &provider.MergeDiff{
			Files:   []*provider.ChangedFile{{NewPath: "a.go"}},
			RawDiff: "diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1,1 +1,2 @@\n+fmt.Println()",
		}
	}
	return f.diff, nil
}

func (f *svcProvider) GetCRFiles(_ context.Context, _, _, _ string) ([]*provider.ChangedFile, error) {
	return nil, errors.New("svcProvider: 未打桩")
}

func (f *svcProvider) CreateNote(_ context.Context, _, _, _ string, body string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.notes = append(f.notes, body)
	return "700", nil
}

func (f *svcProvider) DeleteNote(context.Context, string, string, string, string) error { return nil }

func (f *svcProvider) CreateDiscussion(_ context.Context, _, _, _ string, _ provider.DiscussionOptions) (string, error) {
	return "701", nil
}

func (f *svcProvider) GetFileContent(_ context.Context, _, _, path, _ string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if body, ok := f.files[path]; ok {
		return body, nil
	}
	return "", provider.ErrNotFound
}

func (f *svcProvider) CreateFile(context.Context, string, string, provider.FileOptions) (*provider.FileResult, error) {
	return nil, errors.New("svcProvider: 未打桩")
}

func (f *svcProvider) UpdateFile(context.Context, string, string, provider.FileOptions) (*provider.FileResult, error) {
	return nil, errors.New("svcProvider: 未打桩")
}

func (f *svcProvider) DeleteFile(context.Context, string, string, provider.FileDeleteOptions) (*provider.FileResult, error) {
	return nil, errors.New("svcProvider: 未打桩")
}

func (f *svcProvider) ListNotes(_ context.Context, _, _, _ string) ([]*provider.IssueComment, error) {
	return nil, nil
}

func (f *svcProvider) UpdateNote(context.Context, string, string, string, string, string) (*provider.IssueComment, error) {
	return nil, errors.New("svcProvider: 未打桩")
}

func (f *svcProvider) ListCollaborators(_ context.Context, _, _ string) ([]*provider.Collaborator, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]*provider.Collaborator, 0, len(f.collabs))
	for _, n := range f.collabs {
		out = append(out, &provider.Collaborator{Username: n})
	}
	return out, nil
}

func (f *svcProvider) AddCollaborator(context.Context, string, string, string, provider.AddCollaboratorOptions) error {
	return errors.New("svcProvider: 未打桩")
}

func (f *svcProvider) RemoveCollaborator(context.Context, string, string, string) error {
	return errors.New("svcProvider: 未打桩")
}

func (f *svcProvider) ListIssueReactions(context.Context, string, string, string) ([]*provider.Reaction, error) {
	return nil, nil
}

func (f *svcProvider) AddIssueReaction(_ context.Context, _, _, _, _ string) (*provider.Reaction, error) {
	f.mu.Lock()
	f.reactions++
	f.mu.Unlock()
	return &provider.Reaction{}, nil
}

func (f *svcProvider) RemoveIssueReaction(context.Context, string, string, string, int64) error {
	return nil
}

func (f *svcProvider) ListIssueCommentReactions(context.Context, string, string, int64) ([]*provider.Reaction, error) {
	return nil, nil
}

func (f *svcProvider) AddIssueCommentReaction(_ context.Context, _, _ string, _ int64, _ string) (*provider.Reaction, error) {
	f.mu.Lock()
	f.reactions++
	f.mu.Unlock()
	return &provider.Reaction{}, nil
}

func (f *svcProvider) RemoveIssueCommentReaction(context.Context, string, string, int64, int64) error {
	return nil
}

func (f *svcProvider) ListCRReactions(context.Context, string, string, string) ([]*provider.Reaction, error) {
	return nil, nil
}

func (f *svcProvider) ListCRCommentReactions(context.Context, string, string, int64) ([]*provider.Reaction, error) {
	return nil, nil
}

func (f *svcProvider) AddCRCommentReaction(_ context.Context, _, _ string, _ int64, _ string) (*provider.Reaction, error) {
	f.mu.Lock()
	f.reactions++
	f.mu.Unlock()
	return &provider.Reaction{}, nil
}

func (f *svcProvider) RemoveCRCommentReaction(context.Context, string, string, int64, int64) error {
	return nil
}

func (f *svcProvider) postedNotes() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.notes...)
}

// ---------- harness ----------

type svcHarness struct {
	svc  *Service
	prov *svcProvider
	stop func()
}

func svcTest(t *testing.T, mutate func(cfg *config.Config)) *svcHarness {
	t.Helper()
	store, err := storage.Open(t.TempDir() + "/svc.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	prov := &svcProvider{}
	cfg := &config.Config{BotUsername: "argus-bot"}
	cfg.Platforms = map[string]config.PlatformConfig{
		"gitea": {Token: "tk", Secret: "sec"},
	}
	if mutate != nil {
		mutate(cfg)
	}
	bus := oagentprogress.NewBus[progress.Event]()
	s := &Service{
		Cfg:       cfg,
		Store:     store,
		SDK:       argusctx.NewSDKCtxStubbed(logx.Nop(), map[string]provider.Provider{"gitea": prov}),
		Registry:  dispatch.NewRegistry(),
		Log:       testLogger{},
		Model:     svcModel{},
		LLM:       llm.NewClient(svcModel{}, "svc-stub", nil),
		Version:   "test",
		Bus:       bus,
		EventSeen: progress.NewDedup(256),
	}
	return &svcHarness{svc: s, prov: prov}
}

func requireCode(t *testing.T, what string, got, want int) {
	t.Helper()
	if got != want {
		t.Fatalf("%s: code = %d, want %d", what, got, want)
	}
}

// ---------- TaskAPI ----------

// TestCreateTaskDirectValidation 建任务直通面的校验矩阵：kind 白名单、平台接入、
// 四元组完整性、owner/repo/number 形态白名单、幂等去重。
func TestCreateTaskDirectValidationMatrix(t *testing.T) {
	h := svcTest(t, nil)
	ctx := context.Background()

	if _, code := h.svc.CreateTaskDirect(ctx, domain.TaskKindCodeReview, "gitea", "acme", "app", "42", nil); code != http.StatusOK {
		t.Fatalf("合法任务应 200, got %d", code)
	}
	if _, code := h.svc.CreateTaskDirect(ctx, "bogus-kind", "gitea", "acme", "app", "42", nil); code != http.StatusBadRequest {
		t.Fatalf("未知 kind 应 400, got %d", code)
	}
	if _, code := h.svc.CreateTaskDirect(ctx, domain.TaskKindCodeReview, "nope", "acme", "app", "42", nil); code != http.StatusBadRequest {
		t.Fatalf("未接入平台应 400, got %d", code)
	}
	if _, code := h.svc.CreateTaskDirect(ctx, domain.TaskKindCodeReview, "gitea", "acme", "app", "", nil); code != http.StatusBadRequest {
		t.Fatalf("缺 number 应 400, got %d", code)
	}
	if _, code := h.svc.CreateTaskDirect(ctx, domain.TaskKindCodeReview, "gitea", "acme", "app", "abc", nil); code != http.StatusBadRequest {
		t.Fatalf("非数字 number 应 400, got %d", code)
	}
	if _, code := h.svc.CreateTaskDirect(ctx, domain.TaskKindCodeReview, "gitea", "a cme", "app", "42", nil); code != http.StatusBadRequest {
		t.Fatalf("owner 带空白应 400, got %d", code)
	}
	// 同 PR 在飞 → 幂等去重回执。
	resp, code := h.svc.CreateTaskDirect(ctx, domain.TaskKindCodeReview, "gitea", "acme", "app", "42", nil)
	requireCode(t, "dedup", code, http.StatusOK)
	if resp.GetDuplicated() != true {
		t.Fatalf("在飞任务应幂等去重: %+v", resp)
	}
	// 白名单仓库拒绝。
	h2 := svcTest(t, func(cfg *config.Config) { cfg.RepoAllowlist = []string{"gitea:other/x"} })
	if _, code := h2.svc.CreateTaskDirect(ctx, domain.TaskKindCodeReview, "gitea", "acme", "app", "42", nil); code != http.StatusForbidden {
		t.Fatalf("白名单外应 403, got %d", code)
	}
}

// TestCreateTaskAndDiffTask DTO 入口与 diff 预审任务门控。
func TestCreateTaskAndDiffTask(t *testing.T) {
	h := svcTest(t, nil)
	ctx := context.Background()

	if _, code := h.svc.CreateTask(ctx, &argus.CreateTaskRequest{Kind: argus.TaskKind(99)}); code != http.StatusBadRequest {
		t.Fatalf("非法 IDL kind 应 400, got %d", code)
	}
	resp, code := h.svc.CreateTask(ctx, &argus.CreateTaskRequest{
		Kind: argus.TaskKind_CODE_REVIEW, Platform: "gitea", Owner: "acme", Repo: "app", Number: "43",
	})
	requireCode(t, "CreateTask", code, http.StatusOK)
	if resp.TaskId == "" {
		t.Fatal("应返回 task id")
	}

	// diff 任务：LLM 缺席 503 / 空 diff 400 / 正常 200。
	if _, code := (&Service{Cfg: &config.Config{}, Store: h.svc.Store, Log: testLogger{}}).CreateDiffTask(ctx, "diff"); code != http.StatusServiceUnavailable {
		t.Fatalf("无 LLM 应 503, got %d", code)
	}
	if _, code := h.svc.CreateDiffTask(ctx, "   "); code != http.StatusBadRequest {
		t.Fatalf("空 diff 应 400, got %d", code)
	}
	resp2, code := h.svc.CreateDiffTask(ctx, "diff --git a/x b/x\n+1")
	requireCode(t, "CreateDiffTask", code, http.StatusOK)
	row, err := h.svc.Store.GetTask(ctx, resp2.TaskId)
	if err != nil || row.Kind != domain.TaskKindDiffPreview || row.Platform != "local" {
		t.Fatalf("diff 任务应落库 local/预审: %v %+v", err, row)
	}
}

// TestListTasksVisibility 清单：非法状态 400、local 任务不出清单（可见性隔离）。
func TestListTasksVisibility(t *testing.T) {
	h := svcTest(t, nil)
	ctx := context.Background()
	if _, code := h.svc.ListTasks(ctx, "bogus", 10); code != http.StatusBadRequest {
		t.Fatalf("非法 status 应 400, got %d", code)
	}
	if _, code := h.svc.CreateDiffTask(ctx, "d"); code != http.StatusOK {
		t.Fatal("diff 任务创建失败")
	}
	resp, code := h.svc.ListTasks(ctx, "", 10)
	requireCode(t, "ListTasks", code, http.StatusOK)
	if len(resp.Tasks) == 0 {
		t.Fatal("无约束 token 应看到 local 预审任务")
	}
	// 带仓库约束的 token：local 任务不进清单（taskListable 可见性隔离）。
	tctx := ContextWithTokenRepos(ctx, "gitea:acme/app")
	respC, _ := h.svc.ListTasks(tctx, "", 10)
	for _, it := range respC.Tasks {
		if it.Platform == "local" {
			t.Fatalf("约束 token 下 local 任务不应进清单: %+v", it)
		}
	}
	if _, code := h.svc.ListTasks(ctx, storage.StatusPending, 10); code != http.StatusOK {
		t.Fatal("按状态过滤应 200")
	}
}

// TestGetTaskShapes GetTask：404 / 报告与 findings 透出。
func TestGetTaskShapes(t *testing.T) {
	h := svcTest(t, nil)
	ctx := context.Background()
	if _, code := h.svc.GetTask(ctx, "t-absent"); code != http.StatusNotFound {
		t.Fatalf("缺失任务应 404, got %d", code)
	}
	resp, code := h.svc.GetTask(ctx, mustCreateDoneTask(t, h.svc.Store))
	requireCode(t, "GetTask", code, http.StatusOK)
	if resp.Verdict == nil || *resp.Verdict != "approve" || len(resp.Findings) != 1 {
		t.Fatalf("报告任务投影失真: %+v", resp)
	}
	if resp.Findings[0].EvidenceStatus == nil {
		t.Fatal("findings 应带证据族字段（thrift 对称面）")
	}
}

// TestGetReportByRef 按头 SHA 查报告：空 ref 400 / 未命中 404 / 命中 200。
func TestGetReportByRef(t *testing.T) {
	h := svcTest(t, nil)
	ctx := context.Background()
	if _, code := h.svc.GetReportByRef(ctx, ""); code != http.StatusBadRequest {
		t.Fatalf("空 ref 应 400, got %d", code)
	}
	if _, code := h.svc.GetReportByRef(ctx, "deadbeef"); code != http.StatusNotFound {
		t.Fatalf("未命中应 404, got %d", code)
	}
	id, sha := mustCreateDoneTaskWithSHA(t, h.svc.Store)
	resp, code := h.svc.GetReportByRef(ctx, sha)
	requireCode(t, "GetReportByRef", code, http.StatusOK)
	if resp.TaskId != id || resp.Report == nil || *resp.Report == "" {
		t.Fatalf("报告投影失真: %+v", resp)
	}
	// 约束内命中（列表回退逐条过授权 → 200）。
	tctx := ContextWithTokenRepos(ctx, "gitea:acme/app")
	if _, code = h.svc.GetReportByRef(tctx, sha); code != http.StatusOK {
		t.Fatalf("约束内应命中, got %d", code)
	}
	// 约束外：命中条目但越权 → 403（存在性不泄露语义，与 e2e 门禁一致）。
	tctx2 := ContextWithTokenRepos(ctx, "gitea:other/x")
	if _, code = h.svc.GetReportByRef(tctx2, sha); code != http.StatusForbidden {
		t.Fatalf("约束外应 403, got %d", code)
	}
}

// TestSuppressFindingNear 抑制（file+comment 近重复模式）：空库未命中 matched=false
// + 知识回流/判例桶不炸；非法形态 400；白名单外 403。
func TestSuppressFindingNear(t *testing.T) {
	h := svcTest(t, nil)
	ctx := context.Background()

	if _, code := h.svc.SuppressFinding(ctx, &argus.SuppressFindingRequest{
		Platform: "nope", Owner: "a", Repo: "b", Number: "1",
	}); code != http.StatusBadRequest {
		t.Fatalf("未接入平台应 400, got %d", code)
	}
	if _, code := h.svc.SuppressFinding(ctx, &argus.SuppressFindingRequest{
		Platform: "gitea", Owner: "acme", Repo: "app", Number: "",
	}); code != http.StatusBadRequest {
		t.Fatalf("缺 number 应 400, got %d", code)
	}
	if _, code := h.svc.SuppressFinding(ctx, &argus.SuppressFindingRequest{
		Platform: "gitea", Owner: "acme", Repo: "app", Number: "42", File: strPtr("a.go"),
	}); code != http.StatusBadRequest {
		t.Fatalf("file 无 comment 应 400（形态二选一）, got %d", code)
	}
	resp, code := h.svc.SuppressFinding(ctx, &argus.SuppressFindingRequest{
		Platform: "gitea", Owner: "acme", Repo: "app", Number: "42",
		File: strPtr("a.go"), Comment: strPtr("空指针风险"),
	})
	requireCode(t, "SuppressFinding", code, http.StatusOK)
	if resp.Matched {
		t.Fatal("空库不应命中")
	}
	if resp.Fingerprint == "" {
		t.Fatal("未命中也应回指纹（预插 suppressed 行）")
	}

	// 白名单外写面 403。
	h2 := svcTest(t, func(cfg *config.Config) { cfg.RepoAllowlist = []string{"gitea:other/x"} })
	if _, code := h2.svc.SuppressFinding(ctx, &argus.SuppressFindingRequest{
		Platform: "gitea", Owner: "acme", Repo: "app", Number: "42",
		File: strPtr("a.go"), Comment: strPtr("x"),
	}); code != http.StatusForbidden {
		t.Fatalf("白名单外写面应 403, got %d", code)
	}
}

// TestSuppressFindingFingerprintMode 指纹短码模式：prints 行存在时取回入库身份
// 抑制 + 学习环记账开关。
func TestSuppressFindingFingerprintMode(t *testing.T) {
	h := svcTest(t, func(cfg *config.Config) { cfg.RegretLearning = true })
	ctx := context.Background()
	f := domain.Finding{File: "a.go", Line: 3, Severity: "high", Comment: "裸 return", By: "sast"}
	fp := domain.Fingerprint("a.go", "裸 return", 3)
	if _, err := h.svc.Store.UpsertPrint(ctx, "gitea", "acme", "app", "42", fp, f, "t-1", time.Now()); err != nil {
		t.Fatal(err)
	}
	resp, code := h.svc.SuppressFinding(ctx, &argus.SuppressFindingRequest{
		Platform: "gitea", Owner: "acme", Repo: "app", Number: "42",
		Fingerprint: strPtr(fp[:8]),
	})
	requireCode(t, "SuppressFinding fp", code, http.StatusOK)
	if !resp.Matched {
		t.Fatal("fp 短码应命中入库行")
	}
	// 短码歧义/未命中 → matched=false 200。
	resp2, code := h.svc.SuppressFinding(ctx, &argus.SuppressFindingRequest{
		Platform: "gitea", Owner: "acme", Repo: "app", Number: "42",
		Fingerprint: strPtr("ffffff"),
	})
	requireCode(t, "SuppressFinding fp miss", code, http.StatusOK)
	if resp2.Matched {
		t.Fatal("不存在的短码不应命中")
	}
}

// TestUsageMonthly 用量月报：三口径字段齐备。
func TestUsageMonthly(t *testing.T) {
	h := svcTest(t, func(cfg *config.Config) { cfg.RepoMonthlyTokenBudget = 1000; cfg.RealTokenBudget = 2000 })
	out, code := h.svc.UsageMonthly(context.Background())
	requireCode(t, "UsageMonthly", code, http.StatusOK)
	for _, k := range []string{"month", "total", "budget", "repos", "real_total", "real_budget", "reserved_total"} {
		if _, ok := out[k]; !ok {
			t.Fatalf("用量报告缺 %s: %v", k, out)
		}
	}
}

// TestStatsOverview 形态：days 钳制与聚合骨架。
func TestStatsOverview(t *testing.T) {
	h := svcTest(t, nil)
	out, code := h.svc.StatsOverview(context.Background(), "", "", "", 0)
	requireCode(t, "StatsOverview", code, http.StatusOK)
	if out["window_days"].(int) != 30 {
		t.Fatalf("days<=0 应回 30: %v", out["window_days"])
	}
}

// ---------- webhook 过滤链 ----------

// TestWebhookCRFilterRules §5.1 过滤链规则序：白名单 → bot 自审 → 缺 CR → draft。
func TestWebhookCRFilterRules(t *testing.T) {
	h := svcTest(t, nil)
	skipped := func(reason, _ string) (*argus.WebhookResponse, int) {
		return whResp(whSkipped, reason), http.StatusOK
	}
	ev := func() *provider.NormalizedEvent {
		return &provider.NormalizedEvent{
			Type:  "cr.opened",
			Repo:  &provider.EventRepo{Owner: "acme", Name: "app"},
			CR:    &provider.ChangeRequest{Number: "42"},
			Actor: &provider.CRUser{Username: "alice"},
		}
	}
	if _, _, ok := h.svc.webhookCRFilter(ev(), skipped); !ok {
		t.Fatal("合法 opened 事件应通过")
	}
	// 白名单外。
	if r, _, ok := h.svc.webhookCRFilter(&provider.NormalizedEvent{Type: "issue.opened"}, skipped); ok || r.GetReason() != "event_type" {
		t.Fatalf("白名单外应 skipped(event_type): %+v ok=%v", r, ok)
	}
	// bot 自审。
	e := ev()
	e.Actor.Username = "argus-bot"
	if r, _, ok := h.svc.webhookCRFilter(e, skipped); ok || r.GetReason() != "bot_self" {
		t.Fatalf("bot 自身事件应 skipped(bot_self): %+v", r)
	}
	// 缺 CR。
	e = ev()
	e.CR = nil
	if r, _, ok := h.svc.webhookCRFilter(e, skipped); ok || r.GetReason() != "missing_cr" {
		t.Fatalf("缺 CR 应 skipped(missing_cr): %+v", r)
	}
	// draft 默认不审。
	e = ev()
	e.CR.Draft = true
	h.svc.Cfg.SkipDraft = true
	if r, _, ok := h.svc.webhookCRFilter(e, skipped); ok || r.GetReason() != "draft" {
		t.Fatalf("draft 应 skipped(draft): %+v", r)
	}
}

// TestWebhookCreateTaskFlows CR 建任务：accepted / 同 head SHA 去重 / pipeline 建任务
// 与 in-flight 幂等 / installation 事件直通。
func TestWebhookCreateTaskFlows(t *testing.T) {
	h := svcTest(t, nil)
	ctx := context.Background()
	ev := &provider.NormalizedEvent{
		Type: "cr.opened", Source: "gitea",
		Repo: &provider.EventRepo{Owner: "acme", Name: "app"},
		CR:   &provider.ChangeRequest{Number: "42", HeadSHA: "sha-1"},
	}
	resp, code := h.svc.webhookCreateTask(ctx, ev, "del-1", "gitea")
	requireCode(t, "webhookCreateTask", code, http.StatusOK)
	if resp.Status != whAccepted || resp.GetTaskId() == "" {
		t.Fatalf("应 accepted: %+v", resp)
	}
	// 同 head SHA 二次投递 → duplicate。
	resp2, code := h.svc.webhookCreateTask(ctx, ev, "del-2", "gitea")
	requireCode(t, "dup head", code, http.StatusOK)
	if resp2.Status != whDuplicate {
		t.Fatalf("同 head SHA 应 duplicate: %+v", resp2)
	}

	// pipeline：ci_analysis 任务 + 缺 commit 早退。
	pev := &provider.NormalizedEvent{
		Type: "pipeline.failed", Source: "gitea",
		Repo: &provider.EventRepo{Owner: "acme", Name: "app"}, CommitSHA: "psha-1",
	}
	presp, code := h.svc.pipelineCreateTask(ctx, pev, "pdel-1", "gitea")
	requireCode(t, "pipeline", code, http.StatusOK)
	if presp.Status != whAccepted {
		t.Fatalf("pipeline 应 accepted: %+v", presp)
	}
	// 同 commit in-flight → duplicate。
	presp2, _ := h.svc.pipelineCreateTask(ctx, pev, "pdel-2", "gitea")
	if presp2.Status != whDuplicate {
		t.Fatalf("同 commit 应幂等: %+v", presp2)
	}
	// installation 事件直通。
	inst := h.svc.handleInstallationEvent(&provider.NormalizedEvent{
		Type: "installation", RawPayload: []byte(`{"installation": {"id": 7, "account": {"login": "acme"}}}`),
	}, "idel-1")
	if inst.Status != whAccepted {
		t.Fatalf("installation 应直通 accepted: %+v", inst)
	}
}

// TestDupResp 幂等回执模板：existingID 缺省不指幻影 ID。
func TestDupResp(t *testing.T) {
	r, code := dupResp("同 head SHA 已审查过", "t-1", "del-9")
	requireCode(t, "dupResp", code, http.StatusOK)
	if r.Status != whDuplicate || r.GetTaskId() != "t-1" || r.GetEventId() != "del-9" {
		t.Fatalf("dupResp 形态失真: %+v", r)
	}
	r2, _ := dupResp("x", "", "del-8")
	if r2.GetTaskId() != "" {
		t.Fatal("existingID 空不应回填 TaskId")
	}
}

// ---------- 评论指令分派 ----------

func commentEvent(body, actor string) *provider.NormalizedEvent {
	return &provider.NormalizedEvent{
		Type: "comment.created", Source: "gitea",
		Actor: &provider.CRUser{Username: actor},
		Repo:  &provider.EventRepo{Owner: "acme", Name: "app"},
		CR:    &provider.ChangeRequest{Number: "42"},
		Comment: &provider.IssueComment{
			ID: 77, Body: body, Author: &provider.CRUser{Username: actor},
			CreatedAt: time.Now(),
		},
	}
}

// TestMatchReviewKeywordMatrix 关键词快路径矩阵（否定/疑问/长度守门，与
// comment_router_test 的命中面互补）。
func TestMatchReviewKeywordMatrix(t *testing.T) {
	hit := []string{"重新审查", "请帮我 rerun", "REVIEW 一下请评审", "再审一遍"}
	for _, q := range hit {
		if !matchReviewKeyword(q) {
			t.Errorf("%q 应命中重审关键词", q)
		}
	}
	miss := []string{"这个重新审查的逻辑对吗？", "不要重新审查", "rerunning 是否生效呢", strings.Repeat("长", 40) + "重审"}
	for _, q := range miss {
		if matchReviewKeyword(q) {
			t.Errorf("%q 不应命中（疑问/否定/超长守门）", q)
		}
	}
}

// TestDispatchReviewCmd review 命令：建任务 + 回执评论落 PR；非法 template 截停报错。
func TestDispatchReviewCmd(t *testing.T) {
	h := svcTest(t, nil)
	ctx := context.Background()
	if h.svc.dispatchReview(ctx, "普通提问", "gitea", "acme", "app", "42", "acme/app#42", "d1") {
		t.Fatal("非 review 命令不应消费")
	}
	if !h.svc.dispatchReview(ctx, "review", "gitea", "acme", "app", "42", "acme/app#42", "d1") {
		t.Fatal("review 命令应消费")
	}
	if notes := h.prov.postedNotes(); len(notes) != 1 || !strings.Contains(notes[0], "新一轮全量评审已开始") {
		t.Fatalf("应回执重审受理:\n%v", notes)
	}
	// 非法模板 → 报错回帖，不建任务。
	h.prov.mu.Lock()
	h.prov.notes = nil
	h.prov.mu.Unlock()
	if !h.svc.dispatchReview(ctx, "review --template 自由体", "gitea", "acme", "app", "42", "acme/app#42", "d2") {
		t.Fatal("非法模板也应消费评论")
	}
	if notes := h.prov.postedNotes(); len(notes) != 1 || !strings.Contains(notes[0], "未知报告模板") {
		t.Fatalf("malformed 旗标应报错回帖:\n%v", notes)
	}
}

// TestDispatchInfoCmds help/status 信息类指令：免节流回帖。
func TestDispatchInfoCmds(t *testing.T) {
	h := svcTest(t, nil)
	ctx := context.Background()
	if !h.svc.dispatchInfo(ctx, "help", "gitea", "acme", "app", "42", "acme/app#42", "d1") {
		t.Fatal("help 应消费")
	}
	if !h.svc.dispatchInfo(ctx, "status", "gitea", "acme", "app", "42", "acme/app#42", "d2") {
		t.Fatal("status 应消费")
	}
	notes := h.prov.postedNotes()
	if len(notes) != 2 || !strings.Contains(notes[0], "argus:help") || !strings.Contains(notes[1], "argus:status") {
		t.Fatalf("help/status 应各回一帖:\n%v", notes)
	}
	if !strings.Contains(notes[1], "暂无审查任务") {
		t.Fatalf("空库 status 应提示无任务:\n%s", notes[1])
	}
}

// TestDispatchRecheckCmd recheck 命令：fp 解析失败报错；命中 prints 后走写作命令。
func TestDispatchRecheckCmd(t *testing.T) {
	h := svcTest(t, nil)
	ctx := context.Background()
	if !h.svc.dispatchRecheck(ctx, "recheck fp:xyz", "gitea", "acme", "app", "42", "ref", "d1") {
		t.Fatal("坏 fp 应消费评论")
	}
	if notes := h.prov.postedNotes(); len(notes) != 1 || !strings.Contains(notes[0], "短码格式不对") {
		t.Fatalf("坏 fp 应报错回帖:\n%v", notes)
	}
	// 未命中 prints。
	if !h.svc.dispatchRecheck(ctx, "recheck fp:abcdef", "gitea", "acme", "app", "42", "ref", "d2") {
		t.Fatal("recheck 应消费")
	}
	if notes := h.prov.postedNotes(); len(notes) != 2 || !strings.Contains(notes[1], "未找到匹配的意见") {
		t.Fatalf("未命中应回帖说明:\n%v", notes)
	}
	// 命中 prints → 写作命令走 LLM（罐头）→ 结论回帖。
	f := domain.Finding{File: "a.go", Line: 2, Severity: "high", Comment: "裸 return", By: "sast"}
	fp := domain.Fingerprint("a.go", "裸 return", 2)
	if _, err := h.svc.Store.UpsertPrint(ctx, "gitea", "acme", "app", "42", fp, f, "t-1", time.Now()); err != nil {
		t.Fatal(err)
	}
	if !h.svc.dispatchRecheck(ctx, "recheck fp:"+fp[:8], "gitea", "acme", "app", "42", "ref", "d3") {
		t.Fatal("recheck 应消费")
	}
	notes := h.prov.postedNotes()
	if len(notes) != 3 || !strings.Contains(notes[2], "argus:recheck") {
		t.Fatalf("复核结论应回帖:\n%v", notes)
	}
}

// TestDispatchWritingCmds 写作命令模板：describe/changelog 罐头 LLM 回帖；
// diff 缺失降级回帖。
func TestDispatchWritingCmds(t *testing.T) {
	h := svcTest(t, nil)
	ctx := context.Background()
	if !h.svc.dispatchWriting(ctx, commentEvent("describe", "alice"), "describe",
		"gitea", "42", "ref", "d1") {
		t.Fatal("describe 应消费")
	}
	if !h.svc.dispatchWriting(ctx, commentEvent("changelog", "alice"), "changelog",
		"gitea", "42", "ref", "d2") {
		t.Fatal("changelog 应消费")
	}
	notes := h.prov.postedNotes()
	if len(notes) != 2 || !strings.Contains(notes[0], "argus:describe") || !strings.Contains(notes[1], "argus:changelog") {
		t.Fatalf("写作命令应回帖:\n%v", notes)
	}
	// testgen/fix 未匹配 → false。
	if h.svc.dispatchWriting(ctx, commentEvent("随便聊聊", "alice"), "随便聊聊", "gitea", "42", "ref", "d3") {
		t.Fatal("自由文本不应被写作分派消费")
	}
}

// TestHandleFixGates fix 命令守卫矩阵：未启用 / 无工作副本池 / 未命中指纹。
func TestHandleFixGates(t *testing.T) {
	h := svcTest(t, nil)
	ctx := context.Background()
	h.svc.handleFix(ctx, "gitea", "acme", "app", "42", "ref", "abcdef", "d1")
	if notes := h.prov.postedNotes(); len(notes) != 1 || !strings.Contains(notes[0], "修复功能未启用") {
		t.Fatalf("未启用应回帖说明:\n%v", notes)
	}

	h2 := svcTest(t, func(cfg *config.Config) { cfg.FixAgent = "acpx" })
	h2.svc.handleFix(ctx, "gitea", "acme", "app", "42", "ref", "abcdef", "d2")
	if notes := h2.prov.postedNotes(); len(notes) != 1 || !strings.Contains(notes[0], "工作副本池") {
		t.Fatalf("无池应回帖说明:\n%v", notes)
	}

	h3 := svcTest(t, func(cfg *config.Config) { cfg.FixAgent = "acpx" })
	h3.svc.WC = workcopy.NewPool(t.TempDir(), logx.Nop())
	h3.svc.handleFix(ctx, "gitea", "acme", "app", "42", "ref", "ffffff", "d3")
	if notes := h3.prov.postedNotes(); len(notes) != 1 || !strings.Contains(notes[0], "未找到匹配") {
		t.Fatalf("未命中指纹应回帖说明:\n%v", notes)
	}
}

// TestHandleCommentE2E 评论事件全链：准入 → 回执 👀 → 指令分派（status 免节流）
// → 重复投递幂等。
func TestHandleCommentE2E(t *testing.T) {
	h := svcTest(t, nil)
	ctx := context.Background()
	h.svc.HandleComment(ctx, commentEvent("@argus-bot status", "alice"), "cd-1")
	notes := h.prov.postedNotes()
	if len(notes) != 1 || !strings.Contains(notes[0], "审查状态") {
		t.Fatalf("status 指令应回帖:\n%v", notes)
	}
	// status 属信息类免节流指令：在 ack 回执之前消费，不应有 👀。
	if h.prov.reactions != 0 {
		t.Fatalf("免节流信息类不走 ack 回执面: %d", h.prov.reactions)
	}
	// bot 自身评论防循环。
	h.svc.HandleComment(ctx, commentEvent("@argus-bot status", "argus-bot"), "cd-2")
	if n := len(h.prov.postedNotes()); n != 1 {
		t.Fatalf("bot 自身评论应被吞: notes=%d", n)
	}
	// 重复投递幂等（delivery 已消费）。
	h.svc.HandleComment(ctx, commentEvent("@argus-bot status", "alice"), "cd-1")
	if n := len(h.prov.postedNotes()); n != 1 {
		t.Fatalf("重复投递应幂等跳过: notes=%d", n)
	}
	// 无模型 → 准入拒绝。
	h3 := svcTest(t, nil)
	h3.svc.Model = nil
	h3.svc.HandleComment(ctx, commentEvent("@argus-bot status", "alice"), "cd-3")
	if n := len(h3.prov.postedNotes()); n != 0 {
		t.Fatalf("无模型应静默忽略: notes=%d", n)
	}
}

// TestActorCanDrive 协作门控：open 直通 / collaborator 白名单 / fail-open。
func TestActorCanDrive(t *testing.T) {
	h := svcTest(t, func(cfg *config.Config) { cfg.CommentGate = "open" })
	ctx := context.Background()
	if !h.svc.actorCanDrive(ctx, "gitea", "acme", "app", "alice") {
		t.Fatal("gate=open 应直通")
	}
	h2 := svcTest(t, func(cfg *config.Config) { cfg.CommentGate = "collaborator" })
	h2.prov.mu.Lock()
	h2.prov.collabs = []string{"alice", "bob"}
	h2.prov.mu.Unlock()
	if !h2.svc.actorCanDrive(ctx, "gitea", "acme", "app", "Alice") {
		t.Fatal("协作者（大小写不敏感）应放行")
	}
	if h2.svc.actorCanDrive(ctx, "gitea", "acme", "app", "mallory") {
		t.Fatal("非协作者应拒绝")
	}
	if h2.svc.actorCanDrive(ctx, "gitea", "acme", "app", "") {
		t.Fatal("空 actor 应拒绝")
	}
	// 查询失败 fail-open。
	h3 := svcTest(t, func(cfg *config.Config) { cfg.CommentGate = "collaborator" })
	h3.svc.SDK = argusctx.NewSDKCtxStubbed(logx.Nop(), map[string]provider.Provider{})
	if !h3.svc.actorCanDrive(ctx, "gitea", "acme", "app", "anyone") {
		t.Fatal("平台缺失应 fail-open")
	}
}

// TestHandleSuppressRestoreComments 抑制/恢复评论指令回执文案。
func TestHandleSuppressRestoreComments(t *testing.T) {
	h := svcTest(t, nil)
	ctx := context.Background()
	h.svc.handleSuppressComment(ctx, commentEvent("@argus suppress fp:abcdef", "alice"), 42, "abcdef", "body")
	h.svc.handleRestoreComment(ctx, commentEvent("@argus restore fp:abcdef", "alice"), 42, "abcdef")
	notes := h.prov.postedNotes()
	if len(notes) != 2 {
		t.Fatalf("抑制/恢复各回一帖: %v", notes)
	}
	if !strings.Contains(notes[0], "未找到匹配的意见指纹") {
		t.Fatalf("空库抑制应未命中文案:\n%s", notes[0])
	}
	if !strings.Contains(notes[1], "未找到匹配的已抑制指纹") {
		t.Fatalf("空库恢复应未命中文案:\n%s", notes[1])
	}
}

// TestSubmitCommentGate 评论并发闸：受理 + 忙拒 + 排水。
func TestSubmitCommentGate(t *testing.T) {
	h := svcTest(t, func(cfg *config.Config) { cfg.CommentConcurrency = 1 })
	ctx := context.Background()
	if !h.svc.SubmitComment(ctx, commentEvent("@argus-bot help", "alice"), "sc-1") {
		t.Fatal("首个评论应受理")
	}
	h.svc.DrainComments(5 * time.Second)
	if !h.svc.SubmitComment(ctx, commentEvent("@argus-bot help", "alice"), "sc-2") {
		t.Fatal("排水后应再次受理")
	}
	h.svc.DrainComments(5 * time.Second)
}

// TestRestoreFindingCore 恢复核心：未命中 (false, nil)；有库行命中回 true。
func TestRestoreFindingCore(t *testing.T) {
	h := svcTest(t, nil)
	ctx := context.Background()
	matched, err := h.svc.restoreFindingCore(ctx, "gitea", "acme", "app", "42", "ffffff")
	if err != nil || matched {
		t.Fatalf("空库应未命中: %v %v", matched, err)
	}
}

// ---------- Agent 可观测面 ----------

// TestListAgentsAndRepoConfig 插拔层清单 + 仓库配置视图降级（配置缺失 loaded=false）。
func TestListAgentsAndRepoConfig(t *testing.T) {
	h := svcTest(t, nil)
	resp := h.svc.ListAgents(context.Background(), "gitea", "acme", "app")
	if resp.Agents != nil {
		t.Fatalf("空注册表应空清单: %v", resp.Agents)
	}
	if resp.RepoConfig == nil || resp.RepoConfig.Loaded {
		t.Fatalf("无配置文件应 loaded=false: %+v", resp.RepoConfig)
	}
	// 配置文件缺失走「未加载降级」（无告警，Loaded=false 即可观测）。
}

// TestTaskStagesProjection 阶段时间线：无任务 false；done 任务全链 done。
func TestTaskStagesProjection(t *testing.T) {
	h := svcTest(t, nil)
	ctx := context.Background()
	if _, ok := h.svc.TaskStages(ctx, "t-absent"); ok {
		t.Fatal("缺失任务应 false")
	}
	id := mustCreateDoneTask(t, h.svc.Store)
	out, ok := h.svc.TaskStages(ctx, id)
	if !ok {
		t.Fatal("done 任务应可推导")
	}
	for _, st := range out["stages"].([]map[string]any) {
		if st["done"] != true {
			t.Fatalf("done 任务全阶段应 done: %v", st)
		}
	}
}

// ---------- DTO 与纯函数 ----------

// TestDtoHelpers IDL 映射与形态校验纯函数钉住。
func TestDtoHelpers(t *testing.T) {
	if strPtrOrEmpty("") != nil || *strPtrOrEmpty("x") != "x" {
		t.Fatal("strPtrOrEmpty 空串应 nil")
	}
	if k, err := kindFromIDL(argus.TaskKind_CODE_REVIEW); err != nil || k != domain.TaskKindCodeReview {
		t.Fatalf("kindFromIDL 失真: %v %v", k, err)
	}
	if _, err := kindFromIDL(argus.TaskKind(255)); err == nil {
		t.Fatal("未知 IDL kind 应报错")
	}
	if kindToIDL(plugin.KindCLI) != argus.AgentKind_CLI || kindToIDL("other") != argus.AgentKind_BUILTIN {
		t.Fatal("kindToIDL 失真")
	}
	if protocolToIDL(plugin.ProtocolMCP) != argus.AgentProtocol_MCP || protocolToIDL("other") != argus.AgentProtocol_NONE {
		t.Fatal("protocolToIDL 失真")
	}
	if isPlatformPath("a.b_c-d/e") != true || isPlatformPath("a b") || isPlatformPath("") || isPlatformPath("a\"b") {
		t.Fatal("isPlatformPath 白名单失真")
	}
	if !isDigits("042") || isDigits("") || isDigits("+1") {
		t.Fatal("isDigits 失真")
	}
	if clampInt32(-5) != 0 || clampInt32(1<<40) != int32(^uint32(0)>>1) {
		t.Fatal("clampInt32 钳制失真")
	}
	if p, o, r := parseExactRepoPattern(":gitea:acme/app"); p != "gitea" || o != "acme" || r != "app" {
		t.Fatalf("精确约束解析失真: %s %s %s", p, o, r)
	}
	for _, bad := range []string{"", "gitea:acme/app", ":gitea:acme/app/x", ":gitea:acme/*", "*,x", ":p:o/r,x"} {
		if p, _, _ := parseExactRepoPattern(bad); p != "" {
			t.Fatalf("%q 应非精确形态", bad)
		}
	}
	if fs, ok := taskFindings(`{"findings":[{"file":"a.go","line":1}]}`); !ok || len(fs) != 1 {
		t.Fatal("taskFindings 解析失真")
	}
	if _, ok := taskFindings("not-json"); ok {
		t.Fatal("坏 JSON 应 false")
	}
}

// TestAuditForwardLogger 审计转发 logger 的 Wrap/Info/WithService 面（nil 安全；
// 空 URL = 不外送只走本地 logger）。
func TestAuditForwardLogger(t *testing.T) {
	f := NewAuditForwarder("", "", logx.Nop())
	l := f.Wrap(logx.Nop())
	l.Info("hello", "k", "v")
	l2 := l.WithService("svc2")
	l2.Info("world")
	l3 := f.Wrap(logx.Nop())
	l3.Warn("wrapped")
}

// TestHealthReportAndQA 健康报告 + Q&A 引擎空转面。
func TestHealthReportAndQA(t *testing.T) {
	h := svcTest(t, nil)
	ctx := context.Background()
	if _, code := h.svc.HealthReport(ctx); code != http.StatusOK {
		t.Fatalf("空库健康报告应 200, got %d", code)
	}
	if _, code := (&Service{Log: testLogger{}}).HealthReport(ctx); code != http.StatusInternalServerError {
		t.Fatalf("无 Store 应 500, got %d", code)
	}
	// Q&A 引擎：LLM 失败 → 直答错误回帖路径（用户有感知，不静默）。
	h.svc.Model = svcFailModel{}
	h.svc.Cfg.CommentRouter = false
	h.svc.answerComment(ctx, commentEvent("@argus-bot 这个函数为什么报错", "alice"), "为什么报错",
		"gitea", "42", "ref", "d1")
	if notes := h.prov.postedNotes(); len(notes) != 1 || !strings.Contains(notes[0], "Q&A 错误") {
		t.Fatalf("Q&A 失败应回帖错误说明:\n%v", notes)
	}
}

// TestKnowledgeSuffix 尾注按后端形态分流（无 Milvus = 空尾注）。
func TestKnowledgeSuffix(t *testing.T) {
	h := svcTest(t, nil)
	if h.svc.knowledgeSuffix() != "" {
		t.Fatal("无 Milvus 后端应空尾注")
	}
}

// TestCommentCmdThrottleNoticeText 词表冒烟（与 service 门控文案的耦合点）。
func TestCommentCmdThrottleNoticeText(t *testing.T) {
	if commentcmd.ThrottleNoticeText == "" || commentcmd.GateDeniedText == "" {
		t.Fatal("门控文案词表不应为空")
	}
}

// ---------- 测试任务行 fixture ----------

// mustCreateDoneTask 落一个 done 终态任务（带 verdict/报告/单条意见 result_json）。
func mustCreateDoneTask(t *testing.T, store *storage.Store) string {
	t.Helper()
	id, _ := mustCreateDoneTaskWithSHA(t, store)
	return id
}

func mustCreateDoneTaskWithSHA(t *testing.T, store *storage.Store) (string, string) {
	t.Helper()
	sha := "c0ffee" + fmt.Sprintf("%d", time.Now().UnixNano()%100000)
	task := storage.NewTask(domain.TaskKindCodeReview, "gitea", "acme", "app", "77")
	task.HeadSHA = sha
	if err := store.CreateTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	result, _ := json.Marshal(map[string]any{"findings": []domain.Finding{{
		File: "a.go", Line: 1, Severity: "high", Comment: "空指针", By: "sast",
		Evidence: "a.go:1 nil deref", EvidenceStatus: domain.EvidenceVerified,
	}}})
	// 合成路径（RunnerID 未装配）fencing 跳过，fenced 恒 false——只断言无错误。
	if _, err := store.FinishTaskFenced(context.Background(), task.ID, storage.StatusDone,
		"approve", "## 报告", string(result), "", 100, 0); err != nil {
		t.Fatalf("终态落库失败: %v", err)
	}
	return task.ID, sha
}

// ---------- 影响面 / 整仓地图数据面 ----------

// impactTask 落一个带影响面快照 artifacts 的任务行。
func impactTask(t *testing.T, h *svcHarness, number, sha string) string {
	t.Helper()
	task := storage.NewTask(domain.TaskKindCodeReview, "gitea", "acme", "app", number)
	task.HeadSHA = sha
	if err := h.svc.Store.CreateTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	snap := `{"version":1,"changed":["a.go"],"nodes":[{"id":"file:a.go","kind":"file","label":"a.go","changed":true}],"risk":{"level":"medium","score":40},"stats":{"call_sites":2}}`
	if err := h.svc.Store.MergeTaskArtifacts(context.Background(), task.ID,
		map[string]string{plugin.ArtifactImpactView: snap}); err != nil {
		t.Fatal(err)
	}
	return task.ID
}

// TestImpactDataPlane 影响面数据面：快照 map 投影 + 单页 HTML + 上一轮对比 +
// share 校验器（令牌域隔离：任务令牌开不了地图页）。
func TestImpactDataPlane(t *testing.T) {
	h := svcTest(t, func(cfg *config.Config) { cfg.APIToken = "master-secret" })
	ctx := context.Background()

	// 门面三降级：缺失任务 / 无快照任务。
	if _, ok := h.svc.ImpactView(ctx, "t-absent"); ok {
		t.Fatal("缺失任务应 false")
	}
	plain := storage.NewTask(domain.TaskKindCodeReview, "gitea", "acme", "app", "99")
	plain.HeadSHA = "no-snap"
	_ = h.svc.Store.CreateTask(ctx, plain)
	if _, _, ok := h.svc.impactSnapshot(ctx, plain.ID); ok {
		t.Fatal("无快照应 false")
	}

	id := impactTask(t, h, "42", "sha-42")
	m, ok := h.svc.ImpactView(ctx, id)
	if !ok || m["task_id"] != id || m["task_status"] != storage.StatusPending {
		t.Fatalf("ImpactView 投影失真: %v", m)
	}
	page, ok := h.svc.ImpactViewHTML(ctx, id)
	if !ok || !strings.Contains(page, "🗺 整仓地图") {
		t.Fatalf("ImpactViewHTML 应含互跳链接:\n%.120s", page)
	}

	// 上一轮 done 任务带快照 → compare 段。
	prev := storage.NewTask(domain.TaskKindCodeReview, "gitea", "acme", "app", "42")
	prev.HeadSHA = "sha-prev"
	_ = h.svc.Store.CreateTask(ctx, prev)
	_ = h.svc.Store.MergeTaskArtifacts(ctx, prev.ID, map[string]string{
		plugin.ArtifactImpactView: `{"version":1,"changed":["b.go"],"nodes":[{"id":"file:b.go","kind":"file","label":"b.go","changed":true}],"risk":{"level":"low","score":10}}`,
	})
	_, _ = h.svc.Store.FinishTaskFenced(ctx, prev.ID, storage.StatusDone, "approve", "", "", "", 0, 0)
	m2, ok := h.svc.ImpactView(ctx, id)
	if !ok || m2["compare"] == nil {
		t.Fatalf("上一轮快照应产出对比段: %v", m2["compare"])
	}

	// share 校验器：任务令牌命中任务域；伪造令牌拒绝（经 hertz 路由实际装配）。
	verify := h.svc.ImpactShareVerifier()
	srv := server.New(server.WithHostPorts("127.0.0.1:0"))
	srv.GET("/impact/:taskId", func(ctx context.Context, c *app.RequestContext) {
		if verify(c) {
			c.SetStatusCode(http.StatusOK)
			return
		}
		c.SetStatusCode(http.StatusForbidden)
	})
	tok := impactview.ImpactShareToken("master-secret", id)
	resp := ut.PerformRequest(srv.Engine, http.MethodGet, "/impact/"+id+"?share="+tok, nil)
	if resp.Code != http.StatusOK {
		t.Fatalf("任务域令牌应放行, got %d", resp.Code)
	}
	resp = ut.PerformRequest(srv.Engine, http.MethodGet, "/impact/"+id+"?share=forged", nil)
	if resp.Code != http.StatusForbidden {
		t.Fatalf("伪造令牌应拒绝, got %d", resp.Code)
	}
}

// TestRepoMapPlatformAndVerifier 地图平台解析矩阵与 share 校验器。
func TestRepoMapPlatformAndVerifier(t *testing.T) {
	h := svcTest(t, func(cfg *config.Config) { cfg.APIToken = "master-secret" })
	if p, err := h.svc.RepoMapPlatform("gitea"); err != nil || p != "gitea" {
		t.Fatalf("显式平台应直通: %s %v", p, err)
	}
	if _, err := h.svc.RepoMapPlatform("nope"); err == nil {
		t.Fatal("未知平台应报错")
	}
	if p, err := h.svc.RepoMapPlatform(""); err != nil || p != "gitea" {
		t.Fatalf("唯一平台应缺省: %s %v", p, err)
	}
	h2 := svcTest(t, func(cfg *config.Config) {
		cfg.Platforms["github"] = config.PlatformConfig{Token: "t"}
	})
	if _, err := h2.svc.RepoMapPlatform(""); err == nil {
		t.Fatal("多平台歧义应报错")
	}
	h3 := svcTest(t, func(cfg *config.Config) { cfg.Platforms = nil })
	if _, err := h3.svc.RepoMapPlatform(""); err == nil {
		t.Fatal("零平台应报错")
	}

	// share 校验器：repo-map 域令牌命中（经 hertz 路由实际装配）。
	h3.svc.Cfg.APIToken = "s"
	verify := h3.svc.RepoMapShareVerifier()
	srv := server.New(server.WithHostPorts("127.0.0.1:0"))
	srv.GET("/repos/map", func(ctx context.Context, c *app.RequestContext) {
		if verify(c) {
			c.SetStatusCode(http.StatusOK)
			return
		}
		c.SetStatusCode(http.StatusForbidden)
	})
	tok := impactview.ImpactShareToken("s", repoMapShareID("gitea", "acme", "app"))
	resp := ut.PerformRequest(srv.Engine, http.MethodGet,
		"/repos/map?platform=gitea&owner=acme&repo=app&share="+tok, nil)
	if resp.Code != http.StatusOK {
		t.Fatalf("repo-map 域令牌应放行, got %d", resp.Code)
	}
	resp = ut.PerformRequest(srv.Engine, http.MethodGet,
		"/repos/map?platform=gitea&owner=acme&repo=app&share=bad", nil)
	if resp.Code != http.StatusForbidden {
		t.Fatalf("坏令牌应拒绝, got %d", resp.Code)
	}
	if repoMapShareID("gitea", "acme", "app") != "repo-map:gitea/acme/app" {
		t.Fatal("share 域串失真")
	}
}

// TestRepoMapViewAuthzDenied 地图 MCP 面：白名单外拒绝（authz 收口回归钉）。
func TestRepoMapViewAuthzDenied(t *testing.T) {
	h := svcTest(t, func(cfg *config.Config) { cfg.RepoAllowlist = []string{"gitea:other/x"} })
	out, ok := h.svc.RepoMapView(context.Background(), "gitea", "acme", "app")
	if ok || !strings.Contains(out, "仓库不在") {
		t.Fatalf("白名单外应拒绝: %q %v", out, ok)
	}
	if _, err := h.svc.RepoMapHTML(context.Background(), "gitea", "acme", "app"); err == nil {
		t.Fatal("HTML 面同拒绝语义")
	}
}

// ---------- MCP 知识/检索面（门控与降级） ----------

// TestKnowledgeEndpointGates 数据工具仓库门：白名单外一律拒绝（errRepoDenied）。
func TestKnowledgeEndpointGates(t *testing.T) {
	h := svcTest(t, func(cfg *config.Config) { cfg.RepoAllowlist = []string{"gitea:other/x"} })
	ctx := context.Background()
	for name, fn := range map[string]func() error{
		"SearchSem": func() error { _, err := h.svc.SearchSem(ctx, "gitea", "acme", "app", "42", "q", 5); return err },
		"SearchCode": func() error {
			_, err := h.svc.SearchCode(ctx, "gitea", "acme", "app", "42", "q", "", false)
			return err
		},
		"Trace":       func() error { _, err := h.svc.Trace(ctx, "gitea", "acme", "app", "42", "a", "b"); return err },
		"SymContext":  func() error { _, err := h.svc.SymContext(ctx, "gitea", "acme", "app", "42", "Sym"); return err },
		"AnswerAbout": func() error { _, _ = h.svc.AnswerAboutReview(ctx, "gitea", "acme", "app", "42", "q"); return nil },
	} {
		if err := fn(); err != nil && !strings.Contains(err.Error(), "ARGUS_REPO_ALLOWLIST") {
			t.Fatalf("%s 白名单外应拒绝: %v", name, err)
		}
	}
	if _, ok := h.svc.AnswerAboutReview(ctx, "gitea", "acme", "app", "42", "q"); !ok {
		t.Fatal("拒绝以答案态可见")
	}
	if _, err := h.svc.ReviewDiff(ctx, ""); err == nil {
		t.Fatal("空 diff 应报错（writing 面契约）")
	}
}

// TestSearchFindingsHistory 历史意见检索：空库提示文案 + 命中渲染。
func TestSearchFindingsHistory(t *testing.T) {
	h := svcTest(t, nil)
	ctx := context.Background()
	out, err := h.svc.SearchFindingsHistory(ctx, "gitea", "acme", "app", "空指针", 0)
	if err != nil || !strings.Contains(out, "无匹配的历史意见") {
		t.Fatalf("空库应提示:\n%s %v", out, err)
	}
	f := domain.Finding{File: "a.go", Line: 3, Severity: "high", Comment: "空指针风险", By: "sast"}
	fp := domain.Fingerprint("a.go", "空指针风险", 3)
	if _, err := h.svc.Store.UpsertPrint(ctx, "gitea", "acme", "app", "42", fp, f, "t-1", time.Now()); err != nil {
		t.Fatal(err)
	}
	out2, err := h.svc.SearchFindingsHistory(ctx, "gitea", "acme", "app", "空指针", 10)
	if err != nil || !strings.Contains(out2, "fp:"+fp) {
		t.Fatalf("命中应带指纹:\n%s %v", out2, err)
	}
	// 白名单外拒绝。
	h2 := svcTest(t, func(cfg *config.Config) { cfg.RepoAllowlist = []string{"gitea:other/x"} })
	if _, err := h2.svc.SearchFindingsHistory(ctx, "gitea", "acme", "app", "q", 10); err == nil {
		t.Fatal("白名单外应拒绝")
	}
}

// ---------- 修复同步面与杂项纯函数 ----------

// TestFixFindingEntry MCP 同步修复入口：门控与守卫矩阵（不跑真 agent）。
func TestFixFindingEntry(t *testing.T) {
	h := svcTest(t, nil)
	ctx := context.Background()
	if _, err := h.svc.FixFinding(ctx, "gitea", "acme", "app", "42", "abcdef"); err == nil ||
		!strings.Contains(err.Error(), "ARGUS_FIX_AGENT") {
		t.Fatalf("未启用应报错: %v", err)
	}
	h2 := svcTest(t, func(cfg *config.Config) { cfg.FixAgent = "acpx" })
	h2.svc.WC = workcopy.NewPool(t.TempDir(), logx.Nop())
	if _, err := h2.svc.FixFinding(ctx, "gitea", "acme", "app", "42", "abc"); err == nil {
		t.Fatal("短 fp 应报错")
	}
	if _, err := h2.svc.FixFinding(ctx, "gitea", "acme", "app", "42", "ffffff"); err == nil ||
		!strings.Contains(err.Error(), "未找到匹配") {
		t.Fatalf("未命中指纹应报错: %v", err)
	}
	// 命中 prints 但工作副本不可用（无凭证池）→ fixExec 降级报错。
	f := domain.Finding{File: "a.go", Line: 2, Severity: "high", Comment: "裸 return", By: "sast"}
	fp := domain.Fingerprint("a.go", "裸 return", 2)
	if _, err := h2.svc.Store.UpsertPrint(ctx, "gitea", "acme", "app", "42", fp, f, "t-1", time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := h2.svc.FixFinding(ctx, "gitea", "acme", "app", "42", fp[:8]); err == nil ||
		!strings.Contains(err.Error(), "工作副本不可用") {
		t.Fatalf("工作副本不可用应报错: %v", err)
	}
	// 超时缺省与覆盖。
	if h2.svc.fixAgentTimeout() != fixDefaultTimout {
		t.Fatal("缺省超时应 10m")
	}
	h2.svc.Cfg.FixTimeout = time.Second
	if h2.svc.fixAgentTimeout() != time.Second {
		t.Fatal("覆盖超时应生效")
	}
	// 结果 map 形态（消费契约面）。
	m := fixResultMap(fixFinding{File: "a.go", Line: 2, Severity: "high", Comment: "c"},
		&fixResult{AgentName: "acpx", Fixed: true, Verified: true})
	if m["pushed"] != false || m["verified"] != true || m["agent"] != "acpx" {
		t.Fatalf("fixResultMap 形态失真: %v", m)
	}
}

// TestMiscServiceHelpers actorName/qaThrottle/ackComment/sectionVerified/sweep 纯面。
func TestMiscServiceHelpers(t *testing.T) {
	if actorName(nil) != "" {
		t.Fatal("nil actor 应空")
	}
	if actorName(commentEvent("x", "alice")) != "alice" {
		t.Fatal("actor 提取失真")
	}
	h := svcTest(t, nil)
	if h.svc.qaThrottle() != h.svc.qaThrottle() {
		t.Fatal("节流缓存应单例")
	}
	h.svc.ackComment(context.Background(), "gitea", "acme", "app", 77)
	if h.prov.reactions != 1 {
		t.Fatalf("ackComment 应加 👀: %d", h.prov.reactions)
	}
	h.svc.ackComment(context.Background(), "gitea", "acme", "app", 0) // commentID<=0 跳过
	if h.prov.reactions != 1 {
		t.Fatal("0 评论 ID 不应触发回执")
	}
	if !sectionVerified([]VerifyResult{{OK: true}}) || sectionVerified([]VerifyResult{{OK: false}}) ||
		sectionVerified(nil) {
		t.Fatal("sectionVerified 语义失真")
	}
	// 过期修复沙箱清扫。
	root := t.TempDir()
	old := filepath.Join(root, "fix-old")
	fresh := filepath.Join(root, "fix-new")
	other := filepath.Join(root, "keep")
	for _, d := range []string{old, fresh, other} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	past := time.Now().Add(-3 * time.Hour)
	_ = os.Chtimes(old, past, past)
	sweepStaleFixSandboxes(root)
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatal("过期 fix 沙箱应清扫")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatal("新鲜沙箱应保留")
	}
	sweepStaleFixSandboxes("") // 空根不炸
}

// ---------- 状态查询与 agent 清单 ----------

// TestStatusTextVariants status 文案矩阵：空库/done/running/failed/多轮历史。
func TestStatusTextVariants(t *testing.T) {
	h := svcTest(t, nil)
	ctx := context.Background()
	if out := h.svc.statusText(ctx, "gitea", "acme", "app", "42"); !strings.Contains(out, "暂无审查任务") {
		t.Fatalf("空库文案失真:\n%s", out)
	}
	done := storage.NewTask(domain.TaskKindCodeReview, "gitea", "acme", "app", "42")
	done.HeadSHA = "s1"
	_ = h.svc.Store.CreateTask(ctx, done)
	result, _ := json.Marshal(map[string]any{"findings": []domain.Finding{{File: "a.go"}}})
	_, _ = h.svc.Store.FinishTaskFenced(ctx, done.ID, storage.StatusDone, "approve", "", string(result), "", 0, 0)

	out := h.svc.statusText(ctx, "gitea", "acme", "app", "42")
	if !strings.Contains(out, "approve") || !strings.Contains(out, "1 条意见") {
		t.Fatalf("done 文案失真:\n%s", out)
	}

	run := storage.NewTask(domain.TaskKindCodeReview, "gitea", "acme", "app", "42")
	run.HeadSHA = "s2"
	_ = h.svc.Store.CreateTask(ctx, run)
	if out := h.svc.statusText(ctx, "gitea", "acme", "app", "42"); !strings.Contains(out, "审查进行中") {
		t.Fatalf("running 文案失真:\n%s", out)
	}

	fail := storage.NewTask(domain.TaskKindCodeReview, "gitea", "acme", "app", "42")
	fail.HeadSHA = "s3"
	_ = h.svc.Store.CreateTask(ctx, fail)
	_, _ = h.svc.Store.FinishTaskFenced(ctx, fail.ID, storage.StatusFailed, "", "", "", "LLM 5xx", 0, 0)
	if out := h.svc.statusText(ctx, "gitea", "acme", "app", "42"); !strings.Contains(out, "LLM 5xx") || !strings.Contains(out, "共 3 轮") {
		t.Fatalf("failed/历史文案失真:\n%s", out)
	}
}

// stubPlugin 最小 AgentPlugin（清单 DTO 映射用）。
type stubPlugin struct{ manifest plugin.AgentManifest }

func (p stubPlugin) Manifest() plugin.AgentManifest { return p.manifest }
func (stubPlugin) Invoke(context.Context, plugin.Task) (plugin.Result, error) {
	return plugin.Result{}, nil
}

// TestListAgentsWithRegistry 清单 DTO 映射：kind/protocol/needs/triggers 全景。
func TestListAgentsWithRegistry(t *testing.T) {
	h := svcTest(t, nil)
	_ = h.svc.Registry.Register(&dispatch.Registration{
		Enabled: true,
		Manifest: plugin.AgentManifest{
			Name: "requirement-compliance", Kind: plugin.KindCLI,
			Protocol: plugin.ProtocolMCP, Version: "1.0", Cost: "low",
			Timeout:  30 * time.Second,
			Needs:    plugin.Needs{Diff: true, RepoFileRead: true},
			Triggers: plugin.Trigger{Paths: []string{"*.go"}, Labels: []string{"hotfix"}},
			Skills:   []string{"methodology"},
		},
		Plugin: stubPlugin{},
	})
	resp := h.svc.ListAgents(context.Background(), "", "", "")
	if len(resp.Agents) != 1 {
		t.Fatalf("应有一条清单: %v", resp.Agents)
	}
	a := resp.Agents[0]
	if a.Kind != argus.AgentKind_CLI || a.GetProtocol() != argus.AgentProtocol_MCP ||
		len(a.Needs) != 2 || len(a.TriggerPaths) != 1 || a.GetTimeoutMs() != 30000 {
		t.Fatalf("DTO 映射失真: %+v", a)
	}
	if a.GetCost() != "low" || a.GetVersion() != "1.0" {
		t.Fatalf("可选字段失真: %+v", a)
	}
	// 三参全传附仓库配置视图（缺文件 loaded=false）。
	resp2 := h.svc.ListAgents(context.Background(), "gitea", "acme", "app")
	if resp2.RepoConfig == nil {
		t.Fatal("三参全传应附仓库配置视图")
	}
}

// TestFindingsAndSuggestion 意见投影与短码建议（fp 前缀命中）。
func TestFindingsAndSuggestion(t *testing.T) {
	h := svcTest(t, nil)
	ctx := context.Background()
	task := storage.NewTask(domain.TaskKindCodeReview, "gitea", "acme", "app", "42")
	_ = h.svc.Store.CreateTask(ctx, task)
	fp := domain.Fingerprint("a.go", "空指针", 1)
	result, _ := json.Marshal(map[string]any{"findings": []domain.Finding{{
		File: "a.go", Line: 1, Severity: "high", Comment: "空指针", By: "sast", FP: fp,
	}}})
	_, _ = h.svc.Store.FinishTaskFenced(ctx, task.ID, storage.StatusDone, "comment", "", string(result), "", 0, 0)

	fs, ok := h.svc.Findings(ctx, task.ID)
	if !ok || len(fs) != 1 || fs[0]["fp"] != fp {
		t.Fatalf("意见投影失真: %v", fs)
	}
	got, ok := h.svc.Suggestion(ctx, task.ID, fp[:6])
	if !ok || got["fp"] != fp {
		t.Fatalf("短码建议应命中: %v", got)
	}
	if _, ok := h.svc.Suggestion(ctx, task.ID, "ffffff"); ok {
		t.Fatal("未命中应 false")
	}
	if _, ok := h.svc.Findings(ctx, "t-absent"); ok {
		t.Fatal("缺失任务应 false")
	}
}

// TestAuthzMatrixReadonly 授权矩阵只读导出（e2e 门禁对账面）。
func TestAuthzMatrixReadonly(t *testing.T) {
	rows := AuthzMatrix()
	if len(rows) == 0 {
		t.Fatal("授权矩阵不应为空")
	}
	for _, r := range rows {
		if r.Method == "" || r.Path == "" || r.Gate == "" {
			t.Fatalf("矩阵行字段不全: %+v", r)
		}
	}
}

// ---------- webhook 准入全链（打桩 ParseWebhook） ----------

// hookWebhook 补齐 webhook 面（签名校验按 X-Argus-Sig 头裁决，事件按
// X-Argus-Event 头分发罐头）。
func (f *svcProvider) CreateWebhook(context.Context, provider.CreateWebhookOptions) (*provider.PlatformWebhook, error) {
	return nil, errors.New("svcProvider: 未打桩")
}

func (f *svcProvider) DeleteWebhook(context.Context, string, string, int64) error {
	return errors.New("svcProvider: 未打桩")
}

func (f *svcProvider) ListWebhooks(context.Context, string, string) ([]*provider.PlatformWebhook, error) {
	return nil, errors.New("svcProvider: 未打桩")
}

func (f *svcProvider) ValidateWebhookSignature(r *http.Request, _ string) error {
	if r.Header.Get("X-Argus-Sig") != "ok" {
		return provider.ErrWebhookValidation
	}
	return nil
}

// ParseWebhookEvent 平台语义：签名校验内嵌（SDKCtx.ParseWebhook 直调本方法）。
func (f *svcProvider) ParseWebhookEvent(r *http.Request, _ string) (*provider.NormalizedEvent, error) {
	if r.Header.Get("X-Argus-Sig") != "ok" {
		return nil, provider.ErrWebhookValidation
	}
	switch r.Header.Get("X-Argus-Event") {
	case "cr":
		return &provider.NormalizedEvent{Type: "cr.opened", Source: "gitea",
			Actor: &provider.CRUser{Username: "alice"},
			Repo:  &provider.EventRepo{Owner: "acme", Name: "app"},
			CR:    &provider.ChangeRequest{Number: "42", HeadSHA: "wh-sha"}}, nil
	case "pipeline":
		return &provider.NormalizedEvent{Type: "pipeline.failed", Source: "gitea",
			Repo:      &provider.EventRepo{Owner: "acme", Name: "app"},
			CommitSHA: "p-sha"}, nil
	default:
		return &provider.NormalizedEvent{Type: "comment.created", Source: "gitea",
			Actor: &provider.CRUser{Username: "alice"},
			Repo:  &provider.EventRepo{Owner: "acme", Name: "app"},
			CR:    &provider.ChangeRequest{Number: "42"},
			Comment: &provider.IssueComment{ID: 5, Body: "@argus-bot help",
				Author: &provider.CRUser{Username: "alice"}}}, nil
	}
}

// TestWebhookAdmissionFull webhook 入口全链：body 超限 413 / 签名失败 401 /
// CR 事件 accepted / pipeline accepted / 评论事件 accepted。
func TestWebhookAdmissionFull(t *testing.T) {
	h := svcTest(t, func(cfg *config.Config) { cfg.WebhookMaxBody = 16 })
	srv := server.New(server.WithHostPorts("127.0.0.1:0"))
	srv.POST("/webhook/:platform", func(ctx context.Context, c *app.RequestContext) {
		resp, code := h.svc.Webhook(ctx, c, c.Param("platform"))
		c.SetStatusCode(code)
		if resp != nil {
			_ = json.NewEncoder(c.Response.BodyWriter()).Encode(resp)
		}
	})
	post := func(headers ...ut.Header) *ut.ResponseRecorder {
		hdr := []ut.Header{{Key: "Content-Type", Value: "application/json"}}
		hdr = append(hdr, headers...)
		b := &ut.Body{Body: bytes.NewReader([]byte("{}")), Len: 2} // 每次新 reader（不可重放）
		return ut.PerformRequest(srv.Engine, http.MethodPost, "/webhook/gitea", b, hdr...)
	}
	if resp := post(ut.Header{Key: "X-Argus-Sig", Value: "ok"},
		ut.Header{Key: "Content-Length", Value: "9999"}); resp.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("超限应 413, got %d", resp.Code)
	}
	if resp := post(); resp.Code != http.StatusUnauthorized {
		t.Fatalf("签名失败应 401, got %d body=%s", resp.Code, resp.Body.String())
	}
	for _, ev := range []string{"cr", "pipeline", "comment"} {
		resp := post(ut.Header{Key: "X-Argus-Sig", Value: "ok"},
			ut.Header{Key: "X-Argus-Event", Value: ev},
			ut.Header{Key: "X-Delivery-ID", Value: "d-" + ev})
		if resp.Code != http.StatusOK {
			t.Fatalf("%s 事件应 200, got %d", ev, resp.Code)
		}
		var body argus.WebhookResponse
		_ = json.Unmarshal(resp.Body.Bytes(), &body)
		if body.Status != whAccepted {
			t.Fatalf("%s 事件应 accepted: %+v", ev, body)
		}
	}
}
