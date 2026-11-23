package qa

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/oekit/logx"
	gitmodel "github.com/yi-nology/go-git-platform/model"
	"github.com/yi-nology/go-git-platform/provider"

	"github.com/yi-nology/argus/internal/argusctx"
	"github.com/yi-nology/argus/internal/storage"
)

// ---------- 测试替身 ----------

// fakeModel BaseChatModel 队列：按次弹出回复/错误；记录最后一次入参供断言。
// Stream 恒拒——缺省 DeltaSink nil 时 QA 路径全部走 Generate（OnDelta
// 不挂 → einoChat 走 Generate 分支；流式用例换 streamFakeModel）。
type fakeModel struct {
	mu       sync.Mutex
	contents []string // 与 errs 对位；耗尽后粘住最后一项
	errs     []error
	calls    int
	lastMsgs []*schema.Message
}

func (f *fakeModel) Generate(_ context.Context, msgs []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.lastMsgs = msgs
	if len(f.errs) > 0 {
		err := f.errs[0]
		if len(f.errs) > 1 {
			f.errs = f.errs[1:]
		}
		if err != nil {
			return nil, err
		}
	}
	if len(f.contents) == 0 {
		return &schema.Message{Role: schema.Assistant, Content: ""}, nil
	}
	c := f.contents[0]
	if len(f.contents) > 1 {
		f.contents = f.contents[1:]
	}
	return &schema.Message{Role: schema.Assistant, Content: c}, nil
}

func (f *fakeModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return nil, fmt.Errorf("fakeModel: Stream 未实现")
}

func (f *fakeModel) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *fakeModel) lastUserText() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var b strings.Builder
	for _, m := range f.lastMsgs {
		if m.Role == schema.User {
			b.WriteString(m.Content)
		}
	}
	return b.String()
}

func (f *fakeModel) lastSystemText() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var b strings.Builder
	for _, m := range f.lastMsgs {
		if m.Role == schema.System {
			b.WriteString(m.Content)
		}
	}
	return b.String()
}

// fakePlatform 只实现 QA 触达的 GetCR/GetCRDiff/GetFileContent；其余借嵌入 Provider
// （方法集完整，未覆盖的调用会 panic 兜底——本测试不应触达）。
type fakePlatform struct {
	provider.Provider

	mu    sync.Mutex
	cr    *gitmodel.ChangeRequest
	diff  *gitmodel.MergeDiff
	files map[string]string
	crErr error
}

func (f *fakePlatform) Platform() gitmodel.Platform { return "gitea" }
func (f *fakePlatform) Capabilities() provider.CapabilitySet {
	return provider.CapabilitySet{}
}

func (f *fakePlatform) GetCR(context.Context, string, string, string) (*gitmodel.ChangeRequest, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.crErr != nil {
		return nil, f.crErr
	}
	cp := *f.cr
	return &cp, nil
}

func (f *fakePlatform) GetCRDiff(context.Context, string, string, string) (*gitmodel.MergeDiff, error) {
	return f.diff, nil
}

func (f *fakePlatform) GetFileContent(_ context.Context, _, _, path, _ string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if c, ok := f.files[path]; ok {
		return c, nil
	}
	return "", fmt.Errorf("file not found: %w", provider.ErrNotFound)
}

func newFakePlatform() *fakePlatform {
	return &fakePlatform{
		cr: &gitmodel.ChangeRequest{
			Number: "7", Title: "添加登录功能",
			Description:  "实现账号密码登录",
			State:        gitmodel.CRStateOpened,
			SourceBranch: "feat/login", TargetBranch: "main",
			HeadSHA: "sha-1", Author: &gitmodel.CRUser{Username: "alice"},
		},
		diff: &gitmodel.MergeDiff{
			RawDiff: "diff --git a/main.go b/main.go\n+func login() {}",
			Files: []*gitmodel.ChangedFile{
				{NewPath: "main.go", Additions: 1, Deletions: 0},
				{NewPath: "old.go", IsDeleted: true},
				{NewPath: "new.go", IsNew: true},
			},
		},
		files: map[string]string{
			"main.go": "package main\nfunc login() {}\n",
		},
	}
}

// ---------- 装配 ----------

func newTestEngine(t *testing.T, m model.BaseChatModel) (*Engine, *storage.Store, *fakePlatform) {
	t.Helper()
	store, err := storage.Open(filepath.Join(t.TempDir(), "qa.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	fp := newFakePlatform()
	sdk := argusctx.NewSDKCtxStubbed(testNopLogger{}, map[string]provider.Provider{"gitea": fp})
	e := &Engine{SDK: sdk, Store: store, Model: m, Log: testNopLogger{}}
	return e, store, fp
}

// testNopLogger logx.Logger 空实现（NewSDKCtxStubbed 需要完整接口）。
type testNopLogger struct{}

func (testNopLogger) Debug(string, ...any)                      {}
func (testNopLogger) Info(string, ...any)                       {}
func (testNopLogger) Warn(string, ...any)                       {}
func (testNopLogger) Error(string, ...any)                      {}
func (testNopLogger) Debugf(string, ...any)                     {}
func (testNopLogger) Infof(string, ...any)                      {}
func (testNopLogger) Warnf(string, ...any)                      {}
func (testNopLogger) Errorf(string, ...any)                     {}
func (testNopLogger) CtxDebug(context.Context, string, ...any)  {}
func (testNopLogger) CtxInfo(context.Context, string, ...any)   {}
func (testNopLogger) CtxWarn(context.Context, string, ...any)   {}
func (testNopLogger) CtxError(context.Context, string, ...any)  {}
func (testNopLogger) CtxDebugf(context.Context, string, ...any) {}
func (testNopLogger) CtxInfof(context.Context, string, ...any)  {}
func (testNopLogger) CtxWarnf(context.Context, string, ...any)  {}
func (testNopLogger) CtxErrorf(context.Context, string, ...any) {}
func (testNopLogger) WithCtx(context.Context) logx.Logger       { return testNopLogger{} }
func (testNopLogger) WithService(string) logx.Logger            { return testNopLogger{} }
func (testNopLogger) WithAttrs(...logx.Attr) logx.Logger        { return testNopLogger{} }
func (testNopLogger) WithGroup(string) logx.Logger              { return testNopLogger{} }
func (testNopLogger) SetLevel(logx.Level)                       {}

// ---------- loadContext ----------

// loadContext：标题/描述/作者 + 变更文件形态（新增/删除/增删行数）。
func TestLoadContext_Fields(t *testing.T) {
	e, _, fp := newTestEngine(t, &fakeModel{})
	ctx, err := e.loadContext(context.Background(), "gitea", "acme", "app", "7")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"标题: 添加登录功能",
		"描述: 实现账号密码登录",
		"作者: alice",
		"main.go (+1/-0)",
		"old.go (删除)",
		"new.go (新增)",
	} {
		if !strings.Contains(ctx, want) {
			t.Fatalf("loadContext 缺 %q:\n%s", want, ctx)
		}
	}

	// 空描述 → (无)
	fp.cr.Description = ""
	out, err := e.loadContext(context.Background(), "gitea", "acme", "app", "7")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "描述: (无)") {
		t.Fatalf("空描述应占位:\n%s", out)
	}

	// GetCR 失败
	fp.crErr = fmt.Errorf("boom")
	if _, err := e.loadContext(context.Background(), "gitea", "acme", "app", "7"); err == nil {
		t.Fatal("GetCR 失败应上抛")
	}
}
