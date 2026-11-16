// sdkctx_cover_test.go —— SDKCtx 调用面补测（#36 kernel 60/70）：CR/diff/仓库/
// 文件/评论/反应/协作者/review 调用包装、webhook 解析面、仓库配置装载（404→
// Empty 降级 / 坏 YAML 拒载）、Ready/HasGitHubApp/GitHubAppToken 守卫、可选能力
// 缺席的 unsupported 错误路径（金丝雀：nil 晋升 panic 即用例越界）。
package argusctx

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/oekit/logx"
	"github.com/yi-nology/go-git-platform/provider"

	"github.com/yi-nology/argus/internal/config"
)

// callFake 平台打桩：覆盖被测路径触达的能力面。
type callFake struct {
	provider.Provider

	issue *provider.Issue
	files map[string]string
	repo  *provider.PlatformRepo
}

func (f *callFake) Capabilities() provider.CapabilitySet {
	return provider.DeriveCapabilities(f)
}

func (f *callFake) GetCR(context.Context, string, string, string) (*provider.ChangeRequest, error) {
	return &provider.ChangeRequest{Number: "42", Title: "t"}, nil
}

func (f *callFake) CreateCR(_ context.Context, opts provider.CreateCROptions) (*provider.ChangeRequest, error) {
	return &provider.ChangeRequest{Number: "43", SourceBranch: opts.SourceBranch}, nil
}

func (f *callFake) ListCRs(context.Context, provider.ListCROptions) ([]*provider.ChangeRequest, *int, error) {
	n := 1
	return []*provider.ChangeRequest{{Number: "42"}}, &n, nil
}

func (f *callFake) GetCRDiff(context.Context, string, string, string) (*provider.MergeDiff, error) {
	return &provider.MergeDiff{RawDiff: "diff"}, nil
}

func (f *callFake) GetRepo(context.Context, string, string) (*provider.PlatformRepo, error) {
	if f.repo == nil {
		f.repo = &provider.PlatformRepo{DefaultBranch: "main"}
	}
	return f.repo, nil
}

func (f *callFake) GetFileContent(_ context.Context, _, _, path, _ string) (string, error) {
	if body, ok := f.files[path]; ok {
		return body, nil
	}
	return "", provider.ErrNotFound
}

func (f *callFake) CreateFile(context.Context, string, string, provider.FileOptions) (*provider.FileResult, error) {
	return nil, errors.New("未打桩")
}

func (f *callFake) UpdateFile(context.Context, string, string, provider.FileOptions) (*provider.FileResult, error) {
	return nil, errors.New("未打桩")
}

func (f *callFake) DeleteFile(context.Context, string, string, provider.FileDeleteOptions) (*provider.FileResult, error) {
	return nil, errors.New("未打桩")
}

func (f *callFake) ListNotes(context.Context, string, string, string) ([]*provider.IssueComment, error) {
	return []*provider.IssueComment{{ID: 1, Body: "<!-- argus:start task_id=t-old -->"}}, nil
}

func (f *callFake) UpdateNote(_ context.Context, _, _, _, noteID, body string) (*provider.IssueComment, error) {
	return &provider.IssueComment{Body: body}, nil
}

func (f *callFake) GetCRFiles(context.Context, string, string, string) ([]*provider.ChangedFile, error) {
	return nil, errors.New("未打桩")
}

func (f *callFake) CreateNote(_ context.Context, _, _, _ string, body string) (string, error) {
	return "701", nil
}

func (f *callFake) DeleteNote(context.Context, string, string, string, string) error { return nil }

func (f *callFake) CreateDiscussion(context.Context, string, string, string, provider.DiscussionOptions) (string, error) {
	return "702", nil
}

func (f *callFake) ListIssues(context.Context, provider.ListIssuesOptions) ([]*provider.Issue, *int, error) {
	return nil, nil, errors.New("未打桩")
}

func (f *callFake) GetIssue(context.Context, string, string, string) (*provider.Issue, error) {
	if f.issue == nil {
		f.issue = &provider.Issue{Number: "9", Title: "需求"}
	}
	return f.issue, nil
}

func (f *callFake) CreateIssue(context.Context, provider.CreateIssueOptions) (*provider.Issue, error) {
	return nil, errors.New("未打桩")
}

func (f *callFake) UpdateIssue(context.Context, string, string, string, provider.UpdateIssueOptions) (*provider.Issue, error) {
	return nil, errors.New("未打桩")
}

func (f *callFake) CloseIssue(context.Context, string, string, string) (*provider.Issue, error) {
	return nil, errors.New("未打桩")
}

func (f *callFake) ReopenIssue(context.Context, string, string, string) (*provider.Issue, error) {
	return nil, errors.New("未打桩")
}

func (f *callFake) ListIssueComments(context.Context, string, string, string) ([]*provider.IssueComment, error) {
	return nil, nil
}

func (f *callFake) CreateIssueComment(_ context.Context, _, _, _ string, body string) (*provider.IssueComment, error) {
	return &provider.IssueComment{ID: 8, Body: body}, nil
}

func (f *callFake) UpdateIssueComment(context.Context, string, string, string, int64, string) (*provider.IssueComment, error) {
	return nil, errors.New("未打桩")
}

func (f *callFake) CreateReview(context.Context, string, string, string, provider.CreateReviewOptions) (*provider.ReviewResult, error) {
	return &provider.ReviewResult{}, nil
}

func (f *callFake) ListReviews(context.Context, string, string, string) ([]provider.Review, error) {
	return nil, nil
}

func (f *callFake) GetReview(context.Context, string, string, string, int64) (*provider.Review, error) {
	return nil, errors.New("未打桩")
}

func (f *callFake) RequestReviewers(context.Context, string, string, string, []string) error {
	return errors.New("未打桩")
}

func (f *callFake) DismissReview(context.Context, string, string, string, int64, string) error {
	return errors.New("未打桩")
}

func (f *callFake) CreateWebhook(context.Context, provider.CreateWebhookOptions) (*provider.PlatformWebhook, error) {
	return nil, errors.New("未打桩")
}

func (f *callFake) DeleteWebhook(context.Context, string, string, int64) error {
	return errors.New("未打桩")
}

func (f *callFake) ListWebhooks(context.Context, string, string) ([]*provider.PlatformWebhook, error) {
	return nil, errors.New("未打桩")
}

// ParseWebhookEvent 平台语义：签名校验内嵌（SDKCtx.ParseWebhook 直调本方法）。
func (f *callFake) ParseWebhookEvent(r *http.Request, _ string) (*provider.NormalizedEvent, error) {
	if r.Header.Get("X-Argus-Sig") != "ok" {
		return nil, provider.ErrWebhookValidation
	}
	return &provider.NormalizedEvent{Type: "cr.opened"}, nil
}

func (f *callFake) ValidateWebhookSignature(r *http.Request, _ string) error {
	if r.Header.Get("X-Argus-Sig") != "ok" {
		return provider.ErrWebhookValidation
	}
	return nil
}

func callSDK(t *testing.T, f *callFake) *SDKCtx {
	t.Helper()
	return NewSDKCtxStubbed(logx.Nop(), map[string]provider.Provider{"gitea": f})
}

// TestCallWrappers 调用包装面全景：成功路径字段透传 + 可选能力缺席降级。
func TestCallWrappers(t *testing.T) {
	s := callSDK(t, &callFake{files: map[string]string{".reviewagents.yaml": "use: [x]\n"}})
	ctx := context.Background()

	if cr, err := s.GetCR(ctx, "gitea", "acme", "app", "42"); err != nil || cr.Number != "42" {
		t.Fatalf("GetCR 失真: %v %v", cr, err)
	}
	if cr, err := s.CreateCR(ctx, "gitea", provider.CreateCROptions{SourceBranch: "f"}); err != nil || cr.SourceBranch != "f" {
		t.Fatalf("CreateCR 失真: %v", err)
	}
	if crs, total, err := s.ListCRs(ctx, "gitea", provider.ListCROptions{}); err != nil || total != 1 || len(crs) != 1 {
		t.Fatalf("ListCRs 失真: %v", err)
	}
	if md, err := s.GetCRDiff(ctx, "gitea", "acme", "app", "42"); err != nil || md.RawDiff != "diff" {
		t.Fatalf("GetCRDiff 失真: %v", err)
	}
	if repo, err := s.GetRepo(ctx, "gitea", "acme", "app"); err != nil || repo.DefaultBranch != "main" {
		t.Fatalf("GetRepo 失真: %v", err)
	}
	if body, err := s.GetFileContent(ctx, "gitea", "acme", "app", ".reviewagents.yaml", "main"); err != nil || body == "" {
		t.Fatalf("GetFileContent 失真: %v", err)
	}
	// DiffManager.CreateNote 优先路径（GitLab MR Notes 语义）。
	if ic, err := s.CreateIssueComment(ctx, "gitea", "acme", "app", "42", "hi"); err != nil || ic.ID != 701 {
		t.Fatalf("CreateIssueComment 失真: %v", err)
	}
	if _, err := s.ListIssueComments(ctx, "gitea", "acme", "app", "42"); err != nil {
		t.Fatalf("ListIssueComments 失真: %v", err)
	}
	if _, err := s.CreateReview(ctx, "gitea", "acme", "app", "42", provider.CreateReviewOptions{}); err != nil {
		t.Fatalf("CreateReview 失真: %v", err)
	}
	if _, err := s.ListReviews(ctx, "gitea", "acme", "app", "42"); err != nil {
		t.Fatalf("ListReviews 失真: %v", err)
	}
	// 可选能力缺席（ReactionManager/CollaboratorManager 未实现）→ unsupported 错误路径。
	if err := s.AddIssueCommentReaction(ctx, "gitea", "acme", "app", 1, provider.ReactionEyes); err == nil {
		t.Fatal("无 ReactionManager 应报错")
	}
	if _, err := s.ListCollaborators(ctx, "gitea", "acme", "app"); err == nil {
		t.Fatal("无 CollaboratorManager 应报错")
	}
	// 未知平台。
	if _, err := s.GetCR(ctx, "nope", "a", "b", "1"); err == nil {
		t.Fatal("未知平台应报错")
	}
}

// TestUpdateIssueCommentSwapCreate 更新失败 → swap-create 兜底（删旧保新）。
func TestUpdateIssueCommentSwapCreate(t *testing.T) {
	f := &callFake{}
	s := callSDK(t, f)
	// ListNotes 命中 NoteManager；UpdateNote 成功直通。
	if ic, err := s.UpdateIssueComment(context.Background(), "gitea", "acme", "app", "42", 1, "new"); err != nil || ic == nil {
		t.Fatalf("UpdateNote 直通失真: %v", err)
	}
}

// TestExtractDeliveryID 幂等键提取（平台头族顺序）。
func TestExtractDeliveryID(t *testing.T) {
	s := callSDK(t, &callFake{})
	got := s.ExtractDeliveryID(func(key string) string {
		if key == "X-GitHub-Delivery" {
			return "d-1"
		}
		return ""
	})
	if got != "d-1" {
		t.Fatalf("delivery 提取失真: %q", got)
	}
}

// TestParseWebhookWebhookManager webhook 解析：签名失败 401 形错误 / 成功透传。
func TestParseWebhookWebhookManager(t *testing.T) {
	s := callSDK(t, &callFake{})
	req, _ := http.NewRequest(http.MethodPost, "/webhook", nil)
	if _, err := s.ParseWebhook("gitea", req, "sec"); !errors.Is(err, provider.ErrWebhookValidation) {
		t.Fatalf("签名失败应透出校验错误: %v", err)
	}
	req.Header.Set("X-Argus-Sig", "ok")
	ev, err := s.ParseWebhook("gitea", req, "sec")
	if err != nil || ev.Type != "cr.opened" {
		t.Fatalf("解析透传失真: %v %v", ev, err)
	}
}

// TestLoadRepoConfigShapes 仓库配置装载：404 → Empty 不报错；坏 YAML → Empty +
// 告警；合法文件 → Loaded。
func TestLoadRepoConfigShapes(t *testing.T) {
	// 404：文件缺失。
	s := callSDK(t, &callFake{})
	rc, err := s.LoadRepoConfig(context.Background(), "gitea", "acme", "app", "main")
	if err != nil || rc.Loaded {
		t.Fatalf("404 应 EmptyRepoConfig: %v %+v", err, rc)
	}
	// 坏 YAML。
	s2 := callSDK(t, &callFake{files: map[string]string{".reviewagents.yaml": "use:\n\t- x"}})
	rc2, err := s2.LoadRepoConfig(context.Background(), "gitea", "acme", "app", "main")
	if err != nil || rc2.Loaded {
		t.Fatalf("坏 YAML 应降级 Empty: %v %+v", err, rc2)
	}
	// 合法。
	s3 := callSDK(t, &callFake{files: map[string]string{".reviewagents.yaml": "use: [r1]\n"}})
	rc3, err := s3.LoadRepoConfig(context.Background(), "gitea", "acme", "app", "main")
	if err != nil || !rc3.Loaded || len(rc3.Use) != 1 {
		t.Fatalf("合法配置应装载: %v %+v", err, rc3)
	}
	// LoadRepoConfigAuto：分支空 → GetRepo 缺省分支 → 装载。
	rc4, err := s3.LoadRepoConfigAuto(context.Background(), "gitea", "acme", "app", "")
	if err != nil || !rc4.Loaded {
		t.Fatalf("Auto 两步装载失真: %v %+v", err, rc4)
	}
}

// TestReadyAndGitHubAppGuards 平台清单与 GitHub App 守卫。
func TestReadyAndGitHubAppGuards(t *testing.T) {
	f := &callFake{}
	s := NewSDKCtxStubbed(logx.Nop(), map[string]provider.Provider{"gitea": f})
	s.RegisterProvider("gitee", f) // 追加注册（大小写归一）
	ready := s.Ready()
	if len(ready) != 2 {
		t.Fatalf("Ready 应含两平台: %v", ready)
	}
	if s.HasGitHubApp() {
		t.Fatal("未装配 GitHub App 应 false")
	}
	if tok := s.GitHubAppToken(context.Background()); tok != "" {
		t.Fatalf("未装配 App token 应空: %q", tok)
	}
	if _, err := s.Provider("nope"); err == nil || !strings.Contains(err.Error(), "未配置") {
		t.Fatalf("缺失平台应报未配置: %v", err)
	}
}

// TestUnsupportedErrBanner unsupported 错误文案（平台能力差异的可诊断性）。
func TestUnsupportedErrBanner(t *testing.T) {
	if got := unsupportedErr("gitea", "X"); !strings.Contains(got.Error(), "gitea") || !strings.Contains(got.Error(), "X") {
		t.Fatalf("错误文案失真: %v", got)
	}
}

// TestNewSDKCtxFromConfig 配置构造面（PAT 路径）：token 缺失跳过、secret 缺失
// 告警仍装配、未知平台初始化失败不阻塞启动（学习期内网常态）。
func TestNewSDKCtxFromConfig(t *testing.T) {
	cfg := &config.Config{Platforms: map[string]config.PlatformConfig{
		"gitea":  {Token: "tk", Secret: "sec"},
		"gitee":  {Token: "tk", Secret: ""}, // 无 secret：告警不阻塞
		"github": {Token: ""},               // token 缺失跳过
	}}
	s, err := NewSDKCtx(cfg, logx.Nop())
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Ready()) != 2 {
		t.Fatalf("应装配两平台: %v", s.Ready())
	}
}

// TestNewProviderForUnsupportedUnknown 未知平台构造失败（provider 工厂拒绝）。
func TestNewProviderForUnsupportedUnknown(t *testing.T) {
	if _, err := newProviderFor("no-such-platform", config.PlatformConfig{Token: "t"},
		"t", nil, logx.Nop(), nil); err == nil {
		t.Fatal("未知平台应构造失败")
	}
	// 已知平台 + SkipTLS 构造成功。
	if _, err := newProviderFor("gitea", config.PlatformConfig{Token: "t", SkipTLS: true},
		"t", nil, logx.Nop(), nil); err != nil {
		t.Fatalf("gitea 构造应成功: %v", err)
	}
}
