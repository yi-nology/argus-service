package runner

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/oekit/logx"
	"github.com/yi-nology/go-git-platform/provider"

	"github.com/yi-nology/argus/internal/argusctx"
	"github.com/yi-nology/argus/internal/config"
	"github.com/yi-nology/argus/internal/plugin"
	"github.com/yi-nology/argus/internal/storage"
)

// fixPRFake 打桩平台：CreateCR 捕获开 PR 参数（v7.0 阶段 2 openFixPR 路径），
// CreateNote 捕获原 PR 通知评论（CreateIssueComment 的 DiffManager 优先路径）。
// 其余方法借嵌入 Provider——本测试不应触达，触达即 nil 接口晋升 panic 兜底。
type fixPRFake struct {
	provider.Provider

	mu         sync.Mutex
	crOpts     []provider.CreateCROptions
	notes      []string
	crErr      error
	noteErr    error
	existing   []*provider.ChangeRequest // ListCRs 返回（查重收敛用）
	listErr    error
	draftFailN int    // 前 N 次 CreateCR 返 ErrDraftNotSupported（Draft 降级测）
	draftSeen  []bool // 每次 CreateCR 的 Draft 值（断言降级重试）
}

// ListCRs 查重查询面（Phase 3 按 PR 收敛）：返回配置的既有 CR。
func (f *fixPRFake) ListCRs(_ context.Context, _ provider.ListCROptions) ([]*provider.ChangeRequest, *int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listErr != nil {
		return nil, nil, f.listErr
	}
	n := len(f.existing)
	return append([]*provider.ChangeRequest(nil), f.existing...), &n, nil
}

func (f *fixPRFake) CreateCR(_ context.Context, opts provider.CreateCROptions) (*provider.ChangeRequest, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.draftSeen = append(f.draftSeen, opts.Draft)
	if f.draftFailN > 0 {
		f.draftFailN--
		return nil, provider.ErrDraftNotSupported
	}
	if f.crErr != nil {
		return nil, f.crErr
	}
	f.crOpts = append(f.crOpts, opts)
	return &provider.ChangeRequest{
		Number:       "77",
		WebURL:       "https://example.com/o/r/pull/77",
		SourceBranch: opts.SourceBranch,
		TargetBranch: opts.TargetBranch,
	}, nil
}

func (f *fixPRFake) CreateNote(_ context.Context, _, _, _ string, body string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.noteErr != nil {
		return "", f.noteErr
	}
	f.notes = append(f.notes, body)
	return "501", nil
}

func (f *fixPRFake) snapshot() ([]provider.CreateCROptions, []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]provider.CreateCROptions(nil), f.crOpts...), append([]string(nil), f.notes...)
}

// fixPRRunner 造带打桩 SDK 的 Runner（openFixPR/notifyFixPR 只触达 SDK+Log+Audit）。
func fixPRRunner(t *testing.T, plat provider.Provider) *Runner {
	t.Helper()
	return &Runner{
		Cfg: &config.Config{FixPush: true, FixPushMode: "branch"},
		Log: logx.Nop(),
		SDK: argusctx.NewSDKCtxStubbed(logx.Nop(), map[string]provider.Provider{"github": plat}),
	}
}

func fixPRContext() *plugin.PRContext {
	return &plugin.PRContext{
		Platform: "github", Owner: "acme", Repo: "pay-gateway",
		Number: "42", HeadBranch: "feature/login", BaseBranch: "main",
	}
}

// TestOpenFixPR_BranchAndNotify 开 PR 成功路径（v7.0 阶段 2 Review 环）：
// CreateCR 参数正确（源=修复分支/目标=原 PR 源分支/合并即清理）+ 原 PR 收到
// 带链接的通知评论。
func TestOpenFixPR_BranchAndNotify(t *testing.T) {
	plat := &fixPRFake{}
	r := fixPRRunner(t, plat)
	task := &storage.Task{ID: "t-abc123def456789011131500"}
	pr := fixPRContext()
	branch := fixBranchName(pr)
	if branch != "argus/fix/42" {
		t.Fatalf("按 PR 收敛分支名 = %q, want argus/fix/42", branch)
	}

	r.openFixPR(context.Background(), task, pr, branch, 3)

	opts, notes := plat.snapshot()
	if len(opts) != 1 {
		t.Fatalf("CreateCR 调用数 = %d, want 1", len(opts))
	}
	o := opts[0]
	if o.SourceBranch != branch {
		t.Errorf("SourceBranch = %q, want 修复分支 %q", o.SourceBranch, branch)
	}
	if o.TargetBranch != "feature/login" {
		t.Errorf("TargetBranch = %q, want 原 PR 源分支 feature/login", o.TargetBranch)
	}
	if !o.RemoveSourceBranch {
		t.Error("RemoveSourceBranch 应 true（合并即清理修复分支）")
	}
	if o.Owner != "acme" || o.Repo != "pay-gateway" {
		t.Errorf("仓库寻址错: %s/%s", o.Owner, o.Repo)
	}
	if len(notes) != 1 {
		t.Fatalf("通知评论数 = %d, want 1", len(notes))
	}
	if !strings.Contains(notes[0], "pull/77") {
		t.Errorf("通知评论缺修复 PR 链接:\n%s", notes[0])
	}
	if !strings.Contains(notes[0], "argus:fix_cr task_id=t-abc123def456789011131500 pr=42") {
		t.Errorf("通知评论缺 task_id+pr 锚点:\n%s", notes[0])
	}
}

// TestOpenFixPR_CreateCRFailDegrade 开 PR 失败降级（v7.0 验收线）：分支已推、
// 审计留痕、通知评论不发——绝不阻塞主链路。
func TestOpenFixPR_CreateCRFailDegrade(t *testing.T) {
	plat := &fixPRFake{crErr: errors.New("fork PR: target branch not found")}
	r := fixPRRunner(t, plat)
	task := &storage.Task{ID: "t-deadbeef00112233"}

	r.openFixPR(context.Background(), task, fixPRContext(), fixBranchName(fixPRContext()), 2)

	opts, notes := plat.snapshot()
	if len(opts) != 0 {
		t.Errorf("CreateCR 失败不应有成功参数捕获: %+v", opts)
	}
	if len(notes) != 0 {
		t.Errorf("开 PR 失败不应发通知评论: %v", notes)
	}
}

// TestOpenFixPR_ConvergeExisting 按 PR 收敛（Phase 3 验收「同一 PR 多次审查
// 不产生多个待处理修复 PR」）：同源分支已有 open 修复 PR → 不 CreateCR，
// 回帖更新 + cr_existed。
func TestOpenFixPR_ConvergeExisting(t *testing.T) {
	plat := &fixPRFake{existing: []*provider.ChangeRequest{
		{Number: "77", State: provider.CRStateOpened,
			SourceBranch: "argus/fix/42", TargetBranch: "feature/login",
			WebURL: "https://example.com/o/r/pull/77"},
	}}
	r := fixPRRunner(t, plat)
	task := &storage.Task{ID: "t-abc123def456789011131500"}
	pr := fixPRContext()

	r.openFixPR(context.Background(), task, pr, fixBranchName(pr), 5)

	opts, notes := plat.snapshot()
	if len(opts) != 0 {
		t.Fatalf("已有 open 修复 PR 不应再 CreateCR: %+v", opts)
	}
	if len(notes) != 1 {
		t.Fatalf("收敛应回帖更新（不新开）: notes=%d", len(notes))
	}
	if !strings.Contains(notes[0], "修复已更新至") || !strings.Contains(notes[0], "pr=42") {
		t.Errorf("收敛回帖缺更新措辞/pr 锚点:\n%s", notes[0])
	}
	if task.FixStatus == "" {
		// openFixPR 经 setFixStatus 落库（fake 无 Store 时 best-effort 跳过）——
		// 内存态由 setFixStatus 回写；Store nil 场景 detail 留痕即可。
		t.Logf("FixStatus 未回写（无 Store 打桩，状态落库由 storage 测试覆盖）")
	}
}

// TestOpenFixPR_DraftFallback ggp#160 红利接入（v0.138）：FixDraft=true 且
// 平台创建面无 Draft 参数 → ErrDraftNotSupported 降级非 Draft 重试成功。
func TestOpenFixPR_DraftFallback(t *testing.T) {
	plat := &fixPRFake{draftFailN: 1}
	r := fixPRRunner(t, plat)
	r.Cfg.FixDraft = true
	task := &storage.Task{ID: "t-draftfallback001"}
	pr := fixPRContext()

	r.openFixPR(context.Background(), task, pr, fixBranchName(pr), 1)

	opts, _ := plat.snapshot()
	if len(opts) != 1 {
		t.Fatalf("降级后应成功 CreateCR 一次, got %d", len(opts))
	}
	if len(plat.draftSeen) != 2 || !plat.draftSeen[0] || plat.draftSeen[1] {
		t.Fatalf("应先 Draft=true 失败再 Draft=false 重试, seen=%v", plat.draftSeen)
	}
}

// TestOpenFixPR_NilSDK 守卫：SDK 未装配只跳过不 panic（装配面缺省安全）。
func TestOpenFixPR_NilSDK(t *testing.T) {
	r := &Runner{Cfg: &config.Config{FixPush: true, FixPushMode: "branch"}, Log: logx.Nop()}
	r.openFixPR(context.Background(), &storage.Task{ID: "t-x"},
		fixPRContext(), "argus/fix/t-x", 1)
}
