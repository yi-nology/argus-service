// sdkctx_cover2_test.go —— #36 kernel 60/70 argusctx 补测：评论路由三面
//（Create/Update/Delete 的 NoteManager → IssueManager → DiffManager 兜底序）、
// GetIssue/ListCollaborators 能力面、GitHub App JWT 签发与 installation token
// 源的懒续期三态（新鲜/临期退旧/过期报错）、initGitHubApp 坏凭据启动路径。
// 零网络：所有分支走可配置错误桩或纯本地签名/解析。
package argusctx

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/oekit/logx"
	"github.com/yi-nology/go-git-platform/provider"

	"github.com/yi-nology/argus/internal/config"
)

// ---------- 评论路由三面打桩 ----------

// routeFake DiffManager + IssueManager + NoteManager 三能力全实现，各环节可
// 注错（注释路由的回退序是行为面，错误桩驱动逐分支命中）。
type routeFake struct {
	provider.Provider

	mu sync.Mutex

	// DiffManager 桩参数。
	noteID        string // CreateNote 返回的 note id（可设 "abc" 验不可解析分支）
	noteCreateErr error
	noteDeleteErr error
	deletedNote   string

	// IssueManager 桩参数。
	issueCreateErr error
	issueUpdateErr error
	issueDeleteErr error
	issueBody      string // CreateIssueComment 收到的 body
	issueDeletedID int64

	// NoteManager 桩参数。
	noteUpdateErr error
	updatedBody   string
}

func (f *routeFake) Capabilities() provider.CapabilitySet { return provider.DeriveCapabilities(f) }

func (f *routeFake) GetCR(context.Context, string, string, string) (*provider.ChangeRequest, error) {
	return &provider.ChangeRequest{Number: "42"}, nil
}

func (f *routeFake) GetCRDiff(context.Context, string, string, string) (*provider.MergeDiff, error) {
	return &provider.MergeDiff{}, nil
}

func (f *routeFake) GetCRFiles(context.Context, string, string, string) ([]*provider.ChangedFile, error) {
	return nil, nil
}

func (f *routeFake) CreateNote(context.Context, string, string, string, string) (string, error) {
	if f.noteCreateErr != nil {
		return "", f.noteCreateErr
	}
	if f.noteID == "" {
		return "701", nil
	}
	return f.noteID, nil
}

func (f *routeFake) DeleteNote(_ context.Context, _, _, _ string, noteID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deletedNote = noteID
	return f.noteDeleteErr
}

func (f *routeFake) CreateDiscussion(context.Context, string, string, string, provider.DiscussionOptions) (string, error) {
	return "702", nil
}

func (f *routeFake) ListNotes(context.Context, string, string, string) ([]*provider.IssueComment, error) {
	return nil, nil
}

func (f *routeFake) UpdateNote(_ context.Context, _, _, _, _ string, body string) (*provider.IssueComment, error) {
	if f.noteUpdateErr != nil {
		return nil, f.noteUpdateErr
	}
	f.updatedBody = body
	return &provider.IssueComment{ID: 1, Body: body}, nil
}

func (f *routeFake) ListIssues(context.Context, provider.ListIssuesOptions) ([]*provider.Issue, int, error) {
	return nil, 0, nil
}

func (f *routeFake) GetIssue(context.Context, string, string, string) (*provider.Issue, error) {
	return &provider.Issue{Number: "9", Title: "需求"}, nil
}

func (f *routeFake) CreateIssue(context.Context, provider.CreateIssueOptions) (*provider.Issue, error) {
	return nil, errors.New("未打桩")
}

func (f *routeFake) UpdateIssue(context.Context, string, string, string, provider.UpdateIssueOptions) (*provider.Issue, error) {
	return nil, errors.New("未打桩")
}

func (f *routeFake) CloseIssue(context.Context, string, string, string) (*provider.Issue, error) {
	return nil, errors.New("未打桩")
}

func (f *routeFake) ReopenIssue(context.Context, string, string, string) (*provider.Issue, error) {
	return nil, errors.New("未打桩")
}

func (f *routeFake) ListIssueComments(context.Context, string, string, string) ([]*provider.IssueComment, error) {
	return nil, nil
}

func (f *routeFake) CreateIssueComment(_ context.Context, _, _, _ string, body string) (*provider.IssueComment, error) {
	if f.issueCreateErr != nil {
		return nil, f.issueCreateErr
	}
	f.issueBody = body
	return &provider.IssueComment{ID: 8, Body: body}, nil
}

func (f *routeFake) UpdateIssueComment(context.Context, string, string, string, int64, string) (*provider.IssueComment, error) {
	if f.issueUpdateErr != nil {
		return nil, f.issueUpdateErr
	}
	return &provider.IssueComment{ID: 8}, nil
}

func (f *routeFake) DeleteIssueComment(_ context.Context, _, _, _ string, commentID int64) error {
	if f.issueDeleteErr != nil {
		return f.issueDeleteErr
	}
	f.issueDeletedID = commentID
	return nil
}

func (f *routeFake) ListIssueLabels(context.Context, string, string) ([]*provider.IssueLabel, error) {
	return nil, nil
}

func (f *routeFake) AddIssueLabels(context.Context, string, string, string, []string) error {
	return nil
}

func (f *routeFake) RemoveIssueLabel(context.Context, string, string, string, string) error {
	return nil
}

func (f *routeFake) RemoveIssueLabels(context.Context, string, string, string, []string) error {
	return nil
}

func (f *routeFake) ListCollaborators(context.Context, string, string) ([]*provider.Collaborator, error) {
	return []*provider.Collaborator{}, nil
}

func (f *routeFake) AddCollaborator(context.Context, string, string, string, provider.AddCollaboratorOptions) error {
	return nil
}

func (f *routeFake) RemoveCollaborator(context.Context, string, string, string) error {
	return nil
}

// bareFake 仅核心 Provider 面：NoteManager/IssueManager 缺席（嵌入 provider.Provider
// 会经接口提升天然满足 DiffManager——Provider 内嵌 DiffManager，故 CreateNote/
// DeleteNote 须显式覆写为报错，兜底路径「无删除面」分支只对不嵌接口的真实现可达，
// 单测侧以错误桩走兜底降级路径）。
type bareFake struct {
	provider.Provider
}

func (f *bareFake) GetCR(context.Context, string, string, string) (*provider.ChangeRequest, error) {
	return &provider.ChangeRequest{Number: "42"}, nil
}

func (f *bareFake) Capabilities() provider.CapabilitySet { return provider.DeriveCapabilities(f) }

func (f *bareFake) CreateNote(context.Context, string, string, string, string) (string, error) {
	return "", errors.New("no note surface")
}

func (f *bareFake) DeleteNote(context.Context, string, string, string, string) error {
	return errors.New("no note delete surface")
}

func routeSDK(t *testing.T, f provider.Provider) *SDKCtx {
	t.Helper()
	return NewSDKCtxStubbed(logx.Nop(), map[string]provider.Provider{"gitea": f})
}

// CreateIssueComment 路由序：DiffManager.CreateNote 主路（含 ID 不可解析留痕）
// → CreateNote 失败回退 IssueManager → 双能力缺席报错。
func TestCreateIssueCommentRoutes36(t *testing.T) {
	ctx := context.Background()
	f := &routeFake{}
	s := routeSDK(t, f)

	// 主路：MR Notes API 直建。
	ic, err := s.CreateIssueComment(ctx, "gitea", "acme", "app", "42", "总结正文")
	if err != nil || ic.ID != 701 || ic.Body != "总结正文" {
		t.Fatalf("CreateNote 主路失真: %+v %v", ic, err)
	}

	// note id 不可解析：建成功但 ID=0（幂等更新锚点丢失，留痕可排查）。
	f.noteID = "abc"
	ic2, err := s.CreateIssueComment(ctx, "gitea", "acme", "app", "42", "b")
	if err != nil || ic2.ID != 0 {
		t.Fatalf("不可解析 note id 应 ID=0: %+v %v", ic2, err)
	}

	// CreateNote 失败 → 回退 IssueManager。
	f.noteID = ""
	f.noteCreateErr = errors.New("note api down")
	ic3, err := s.CreateIssueComment(ctx, "gitea", "acme", "app", "42", "fallback")
	if err != nil || ic3.ID != 8 || f.issueBody != "fallback" {
		t.Fatalf("回退 IssueManager 失真: %+v %v", ic3, err)
	}

	// NoteManager/IssueManager 双缺席形态（bareFake 仅剩 CreateNote 报错面）：
	// 主路报错回退 IssueManager 再报错——错误透出可诊断，不静默吞。
	if _, err := routeSDK(t, &bareFake{}).CreateIssueComment(ctx, "gitea", "acme", "app", "42", "x"); err == nil ||
		!strings.Contains(err.Error(), "不支持 DiffManager 也不支持 IssueManager") {
		t.Fatalf("回退终点应明确报错: %v", err)
	}
}

// UpdateIssueComment 路由序：NoteManager 原地更新 → IssueManager → DiffManager
// 换新兜底（CreateNote + DeleteNote，返回新 ID）。
func TestUpdateIssueCommentRoutes36(t *testing.T) {
	ctx := context.Background()

	// 主路：NoteManager 原地更新。
	f := &routeFake{}
	s := routeSDK(t, f)
	if ic, err := s.UpdateIssueComment(ctx, "gitea", "acme", "app", "42", 1, "new"); err != nil ||
		ic == nil || f.updatedBody != "new" {
		t.Fatalf("NoteManager 直更失真: %+v %v", ic, err)
	}

	// NoteManager 失败 → IssueManager 直更。
	f.noteUpdateErr = errors.New("note update down")
	if ic, err := s.UpdateIssueComment(ctx, "gitea", "acme", "app", "42", 8, "via-issue"); err != nil || ic.ID != 8 {
		t.Fatalf("IssueManager 直更失真: %+v %v", ic, err)
	}

	// 双失败 → 换新兜底：新建后删旧，返回新 ID。
	f.issueUpdateErr = errors.New("issue update down")
	ic, err := s.UpdateIssueComment(ctx, "gitea", "acme", "app", "42", 8, "swapped")
	if err != nil || ic.ID != 701 || f.deletedNote != "8" {
		t.Fatalf("换新兜底失真: ic=%+v deleted=%q err=%v", ic, f.deletedNote, err)
	}

	// 换新时新 note id 不可解析：删旧保新、ID=0（缓存定位失效属可容忍降级）。
	f.noteID = "junk"
	if ic, err := s.UpdateIssueComment(ctx, "gitea", "acme", "app", "42", 8, "x"); err != nil || ic.ID != 0 {
		t.Fatalf("换新 ID 不可解析应 ID=0: %+v %v", ic, err)
	}

	// 换新建失败：回落原错误（调用方维持既有降级语义）。
	f.noteID = ""
	f.noteCreateErr = errors.New("swap create down")
	if _, err := s.UpdateIssueComment(ctx, "gitea", "acme", "app", "42", 8, "x"); err == nil ||
		!strings.Contains(err.Error(), "issue update down") {
		t.Fatalf("换新建失败应回落原错误: %v", err)
	}

	// IssueManager 缺席形态：换新兜底 CreateNote 报错 → 回落原错误。
	if _, err := routeSDK(t, &bareFake{}).UpdateIssueComment(ctx, "gitea", "acme", "app", "42", 1, "x"); err == nil {
		t.Fatal("兜底链失败应报错")
	}
}

// DeleteIssueComment 路由：IssueManager 主路 → DiffManager.DeleteNote 兜底 →
// 双缺席明确报错。
func TestDeleteIssueCommentRoutes36(t *testing.T) {
	ctx := context.Background()
	f := &routeFake{}
	s := routeSDK(t, f)

	if err := s.DeleteIssueComment(ctx, "gitea", "acme", "app", "42", 55); err != nil || f.issueDeletedID != 55 {
		t.Fatalf("IssueManager 主路失真: deleted=%d err=%v", f.issueDeletedID, err)
	}

	// 主路失败 → DiffManager.DeleteNote 兜底（note id 十进制）。
	f.issueDeleteErr = errors.New("issue delete down")
	if err := s.DeleteIssueComment(ctx, "gitea", "acme", "app", "42", 66); err != nil || f.deletedNote != "66" {
		t.Fatalf("DiffManager 兜底失真: note=%q err=%v", f.deletedNote, err)
	}

	// 双失败：错误透出。
	f.noteDeleteErr = errors.New("note delete down")
	if err := s.DeleteIssueComment(ctx, "gitea", "acme", "app", "42", 66); err == nil ||
		!strings.Contains(err.Error(), "note delete down") {
		t.Fatalf("双失败应透错: %v", err)
	}

	// IssueManager 缺席形态（bareFake）：兜底 DeleteNote 报错透出（「无删除面」
	// 终点分支对嵌接口打桩不可达，见 bareFake 注释——生产侧由真实现覆盖）。
	if err := routeSDK(t, &bareFake{}).DeleteIssueComment(ctx, "gitea", "acme", "app", "42", 1); err == nil ||
		!strings.Contains(err.Error(), "no note delete surface") {
		t.Fatalf("兜底失败应透错: %v", err)
	}
}

// GetIssue / ListCollaborators 能力面：实现命中直通；平台缺失报未配置。
func TestGetIssueAndCollaborators36(t *testing.T) {
	ctx := context.Background()
	f := &routeFake{}
	s := routeSDK(t, f)

	issue, err := s.GetIssue(ctx, "gitea", "acme", "app", "9")
	if err != nil || issue == nil || issue.Title != "需求" {
		t.Fatalf("GetIssue 失真: %+v %v", issue, err)
	}
	if _, err := s.GetIssue(ctx, "nope", "a", "b", "1"); err == nil {
		t.Fatal("未知平台应报错")
	}

	// CollaboratorManager 成功路径（此前只有缺席路径）：桩直通不报错。
	if cs, err := s.ListCollaborators(ctx, "gitea", "acme", "app"); err != nil || len(cs) != 0 {
		t.Fatalf("ListCollaborators 桩直通失真: %v %v", cs, err)
	}
}

// ---------- GitHub App：JWT 签发与 token 源懒续期 ----------

func genRSAKey36(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// signJWT 产出可验签的三段式 JWT：header/claims 形态 + RS256 签名可验证。
func TestSignJWT36(t *testing.T) {
	key := genRSAKey36(t)
	now := time.Now()
	tok, err := signJWT(4242, now, key)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("JWT 应三段: %d", len(parts))
	}
	dec := func(s string) []byte {
		b, err := base64.RawURLEncoding.DecodeString(s)
		if err != nil {
			t.Fatalf("段解码失败: %v", err)
		}
		return b
	}
	var header map[string]string
	if err := json.Unmarshal(dec(parts[0]), &header); err != nil {
		t.Fatal(err)
	}
	if header["alg"] != "RS256" || header["typ"] != "JWT" {
		t.Fatalf("header 失真: %v", header)
	}
	var claims map[string]int64
	if err := json.Unmarshal(dec(parts[1]), &claims); err != nil {
		t.Fatal(err)
	}
	if claims["iss"] != 4242 {
		t.Fatalf("iss 失真: %v", claims)
	}
	if claims["exp"]-claims["iat"] != 9*60+10 { // iat 回拨 10s、exp = now+9min → 窗口 550s
		t.Fatalf("iat/exp 窗口失真: %v", claims)
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, h[:], sig); err != nil {
		t.Fatalf("RS256 验签失败: %v", err)
	}
}

// generateInstallationToken 凭据解析错误面（不触网）：base64 解码失败 / 无
// PEM 块 / PKCS8 非 RSA 私钥。
func TestGenerateInstallationTokenBadCreds36(t *testing.T) {
	if _, _, err := generateInstallationToken(1, "!!!not-base64!!!", 2); err == nil ||
		!strings.Contains(err.Error(), "base64") {
		t.Fatalf("非 base64 非 PEM 应报 base64 错: %v", err)
	}
	if _, _, err := generateInstallationToken(1, base64.StdEncoding.EncodeToString([]byte("garbage")), 2); err == nil ||
		!strings.Contains(err.Error(), "PEM") {
		t.Fatalf("垃圾数据应报 PEM 解析错: %v", err)
	}
	// 合法 PEM 且 PKCS8 可解析但非 RSA（ECDSA 私钥）。
	eck, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(eck)
	if err != nil {
		t.Fatal(err)
	}
	p := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	if _, _, err := generateInstallationToken(1, string(p), 2); err == nil ||
		!strings.Contains(err.Error(), "非 RSA") {
		t.Fatalf("ECDSA 私钥应报非 RSA: %v", err)
	}
}

// installationTokenSource 三态：新鲜直用 / 临期刷新失败退旧值 / 过期刷新失败
// 报错——懒续期的可用性优先语义。
func TestInstallationTokenSource36(t *testing.T) {
	pc := config.PlatformConfig{
		GitHubAppID: 1, GitHubAppPrivateKey: "bad-pem", GitHubAppInstallationID: 2,
	}
	ctx := context.Background()

	// 新鲜（>5min 余量）：直用现值，零网络。
	ts := newInstallationTokenSource(pc, "tok", time.Now().Add(time.Hour), logx.Nop())
	if got, err := ts.Token(ctx); err != nil || got != "tok" {
		t.Fatalf("新鲜 token 失真: %q %v", got, err)
	}

	// 临期（<5min）：触发刷新，坏凭据失败 → 旧 token 仍在寿命内退旧值。
	ts2 := newInstallationTokenSource(pc, "old", time.Now().Add(time.Minute), logx.Nop())
	if got, err := ts2.Token(ctx); err != nil || got != "old" {
		t.Fatalf("临期应退旧值: %q %v", got, err)
	}

	// 已过期 + 刷新失败：错误上抛（调用方回落 PAT）。
	ts3 := newInstallationTokenSource(pc, "stale", time.Now().Add(-time.Minute), logx.Nop())
	if _, err := ts3.Token(ctx); err == nil || !strings.Contains(err.Error(), "installation token 获取失败") {
		t.Fatalf("过期刷新失败应报错: %v", err)
	}
}

// GitHubAppToken：已装配源取值 / 源失败回落空串（调用方回落 PAT）。
func TestGitHubAppTokenWithSource36(t *testing.T) {
	pc := config.PlatformConfig{GitHubAppID: 1, GitHubAppPrivateKey: "bad", GitHubAppInstallationID: 2}
	s := NewSDKCtxStubbed(logx.Nop(), nil)
	if tok := s.GitHubAppToken(context.Background()); tok != "" {
		t.Fatalf("未装配应空: %q", tok)
	}
	s.appTokens = newInstallationTokenSource(pc, "fresh", time.Now().Add(time.Hour), logx.Nop())
	if tok := s.GitHubAppToken(context.Background()); tok != "fresh" {
		t.Fatalf("装配后应取源值: %q", tok)
	}
	s.appTokens = newInstallationTokenSource(pc, "stale", time.Now().Add(-time.Minute), logx.Nop())
	if tok := s.GitHubAppToken(context.Background()); tok != "" {
		t.Fatalf("源失败应空串回落 PAT: %q", tok)
	}
}

// initGitHubApp 坏凭据：启动期首取失败 → 平台判死不装配（fail-fast 语义），
// 不阻塞其他平台。
func TestInitGitHubAppBadCreds36(t *testing.T) {
	cfg := &config.Config{SDKCallTimeout: 0, Platforms: map[string]config.PlatformConfig{
		"github": {GitHubAppID: 1, GitHubAppPrivateKey: "bad-pem", GitHubAppInstallationID: 2},
		"gitea":  {Token: "tk", Secret: "sec"},
	}}
	s, err := NewSDKCtx(cfg, logx.Nop())
	if err != nil {
		t.Fatal(err)
	}
	if s.HasGitHubApp() {
		t.Fatal("坏凭据不应装配 App token 源")
	}
	ready := s.Ready()
	if len(ready) != 1 || ready[0] != "gitea" {
		t.Fatalf("github 判死后应仅 gitea 在册: %v", ready)
	}
}

// bound 兜底超时三态：已有 deadline 透传 / 缺省 120s / 显式关闭。
func TestBoundTimeout36(t *testing.T) {
	s := NewSDKCtxStubbed(logx.Nop(), nil)
	dctx, dcancel := context.WithTimeout(context.Background(), time.Second)
	defer dcancel()
	bctx, bcancel := s.bound(dctx)
	if _, ok := bctx.Deadline(); !ok || bctx != dctx {
		t.Fatal("已有 deadline 应原样透传")
	}
	bcancel()

	mctx, mcancel := s.bound(context.Background())
	if d, ok := mctx.Deadline(); !ok || time.Until(d) > defaultSDKCallTimeout {
		t.Fatalf("缺省兜底超时应 ≤120s: %v", d)
	}
	mcancel()

	s.sdkCallTimeout = -1
	nctx, ncancel := s.bound(context.Background())
	if _, ok := nctx.Deadline(); ok {
		t.Fatal("显式关闭不应设 deadline")
	}
	ncancel()
}

// ParseWebhookEvent 平台缺认声明：Provider 直接满足 webhook 解析能力即可
// 直调（编译期锚定该面不被误删）。
var _ interface {
	ParseWebhookEvent(*http.Request, string) (*provider.NormalizedEvent, error)
} = &routeFake{}
