package mcpserver

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/yi-nology/argus/internal/config"
)

// ---------- suppress_finding（v3.128.0） ----------

// errSuppressScope 数据面 admin 门拒绝的测试替身错误。
var errSuppressScope = errors.New("suppress_finding 需 admin 档 token")

// fakeSuppressor 实现 FindingSuppressor。
type fakeSuppressor struct {
	fakeCore
	gotFP string
	out   map[string]any
	err   error
}

func (f *fakeSuppressor) SuppressFindingScoped(ctx context.Context, _, _, _, _, fp string) (map[string]any, error) {
	f.gotFP = fp
	return f.out, f.err
}

func TestSuppressFinding_RegisteredAndCalled(t *testing.T) {
	core := &fakeSuppressor{out: map[string]any{"matched": true, "fingerprint": "abcdef012345"}}
	srv := New(core, "v-test")
	found := false
	for _, n := range listToolNames(t, srv) {
		if n == "suppress_finding" {
			found = true
		}
	}
	if !found {
		t.Fatal("实现 FindingSuppressor 后应注册 suppress_finding")
	}
	text, isErr := callTool(t, srv, "suppress_finding", map[string]any{
		"platform": "gitlab", "owner": "acme", "repo": "app", "number": "2", "fp": "AB12CD34",
	})
	if isErr {
		t.Fatalf("suppress_finding 报错: %s", text)
	}
	if core.gotFP != "ab12cd34" {
		t.Errorf("fp 应转小写透传: %q", core.gotFP)
	}
	if !strings.Contains(text, "matched") {
		t.Errorf("输出不符:\n%s", text)
	}
	// fp 非法短码 → 工具层拦截；数据面 error → isError（如 admin 门拒绝）
	_, isErr = callTool(t, srv, "suppress_finding", map[string]any{
		"platform": "gitlab", "owner": "acme", "repo": "app", "number": "2", "fp": "ab",
	})
	if !isErr {
		t.Error("短 fp 应 isError")
	}
	core2 := &fakeSuppressor{err: errSuppressScope}
	srv2 := New(core2, "v-test")
	text, isErr = callTool(t, srv2, "suppress_finding", map[string]any{
		"platform": "gitlab", "owner": "acme", "repo": "app", "number": "2", "fp": "ab12cd34",
	})
	if !isErr || !strings.Contains(text, "admin") {
		t.Errorf("scope 拒绝应 isError 含 admin: isErr=%v text=%s", isErr, text)
	}
}

// ---------- get_capabilities（v3.129.0） ----------

// fakeCaps 实现 CapabilityFace。
type fakeCaps struct {
	fakeCore
	out map[string]any
}

func (f *fakeCaps) Capabilities(context.Context) map[string]any { return f.out }

func TestGetCapabilities_RegisteredAndCalled(t *testing.T) {
	core := &fakeCaps{out: map[string]any{"version": "v-test", "fix_agent": false, "sem_search": true}}
	srv := New(core, "v-test")
	found := false
	for _, n := range listToolNames(t, srv) {
		if n == "get_capabilities" {
			found = true
		}
	}
	if !found {
		t.Fatal("实现 CapabilityFace 后应注册 get_capabilities")
	}
	text, isErr := callTool(t, srv, "get_capabilities", map[string]any{})
	if isErr {
		t.Fatalf("get_capabilities 报错: %s", text)
	}
	for _, want := range []string{"v-test", "fix_agent", "sem_search"} {
		if !strings.Contains(text, want) {
			t.Errorf("输出缺 %q:\n%s", want, text)
		}
	}
}

// ---------- restore_finding（v3.131.0） ----------

type fakeRestorer struct {
	fakeCore
	gotFP string
	out   map[string]any
	err   error
}

func (f *fakeRestorer) RestoreFindingScoped(_ context.Context, _, _, _, _, fp string) (map[string]any, error) {
	f.gotFP = fp
	return f.out, f.err
}

func TestRestoreFinding_RegisteredAndCalled(t *testing.T) {
	core := &fakeRestorer{out: map[string]any{"matched": true}}
	srv := New(core, "v-test")
	found := false
	for _, n := range listToolNames(t, srv) {
		if n == "restore_finding" {
			found = true
		}
	}
	if !found {
		t.Fatal("实现 FindingRestorer 后应注册 restore_finding")
	}
	text, isErr := callTool(t, srv, "restore_finding", map[string]any{
		"platform": "gitlab", "owner": "acme", "repo": "app", "number": "2", "fp": "AB12CD34",
	})
	if isErr {
		t.Fatalf("restore_finding 报错: %s", text)
	}
	if core.gotFP != "ab12cd34" {
		t.Errorf("fp 应转小写透传: %q", core.gotFP)
	}
	// 短 fp 工具层拦；数据面 admin 门错误透传
	_, isErr = callTool(t, srv, "restore_finding", map[string]any{
		"platform": "gitlab", "owner": "acme", "repo": "app", "number": "2", "fp": "ab",
	})
	if !isErr {
		t.Error("短 fp 应 isError")
	}
	core2 := &fakeRestorer{err: errSuppressScope}
	srv2 := New(core2, "v-test")
	text, isErr = callTool(t, srv2, "restore_finding", map[string]any{
		"platform": "gitlab", "owner": "acme", "repo": "app", "number": "2", "fp": "ab12cd34",
	})
	if !isErr || !strings.Contains(text, "admin") {
		t.Errorf("scope 拒绝应 isError 含 admin: isErr=%v text=%s", isErr, text)
	}
}

// ---------- search_findings（v3.138.0） ----------

type fakeHistorian struct {
	fakeCore
	query string
	limit int
	out   string
	err   error
}

func (f *fakeHistorian) SearchFindingsHistory(_ context.Context, _, _, _, query string, limit int) (string, error) {
	f.query, f.limit = query, limit
	return f.out, f.err
}

func TestSearchFindings_RegisteredAndCalled(t *testing.T) {
	core := &fakeHistorian{out: "search_findings 命中 1 条"}
	srv := New(core, "v-test")
	found := false
	for _, n := range listToolNames(t, srv) {
		if n == "search_findings" {
			found = true
		}
	}
	if !found {
		t.Fatal("实现 FindingHistorian 后应注册 search_findings")
	}
	text, isErr := callTool(t, srv, "search_findings", map[string]any{
		"platform": "gitlab", "owner": "acme", "repo": "app", "query": "硬编码", "limit": float64(5),
	})
	if isErr {
		t.Fatalf("search_findings 报错: %s", text)
	}
	if core.query != "硬编码" || core.limit != 5 {
		t.Errorf("入参透传错: %q %d", core.query, core.limit)
	}
}

// TestPlatformVocabSync 平台词表防漂移：mcpserver 描述词表（platformVocab）与
// config.SupportedPlatforms 必须双向相等——mcpserver 生产面零 internal 依赖，
// 单源在 config，此处钉一致性（config 新增平台而此处漏 = 描述口径重回散落）。
func TestPlatformVocabSync(t *testing.T) {
	got := map[string]bool{}
	for _, p := range strings.Split(platformVocab, "/") {
		got[p] = true
	}
	for _, p := range config.SupportedPlatforms {
		if !got[p] {
			t.Fatalf("config.SupportedPlatforms 的 %q 未进 platformVocab（%q）", p, platformVocab)
		}
	}
	if len(got) != len(config.SupportedPlatforms) {
		t.Fatalf("platformVocab 与 config.SupportedPlatforms 集合不等: %q vs %v", platformVocab, config.SupportedPlatforms)
	}
}
