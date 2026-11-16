// comment_resolver_test.go —— 评论型 Key 的解析规则与判定边界。
package effect

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/yi-nology/argus/internal/storage"
)

// fakeCommentFinder 记录调用参数并返回预设结果。
type fakeCommentFinder struct {
	id    int64
	found bool
	err   error
	calls []struct {
		platform, owner, repo, number, taskID, headSHA string
	}
}

func (f *fakeCommentFinder) FindTaskComment(_ context.Context, platform, owner, repo, number, taskID, headSHA string) (int64, bool, error) {
	f.calls = append(f.calls, struct{ platform, owner, repo, number, taskID, headSHA string }{platform, owner, repo, number, taskID, headSHA})
	return f.id, f.found, f.err
}

func TestParseCommentKey(t *testing.T) {
	valid := map[string]commentKey{
		"comment:summary:t-1:sha-1":   {kind: "summary", taskID: "t-1", headSHA: "sha-1"},
		"comment:summary:t-1:":        {kind: "summary", taskID: "t-1", headSHA: ""},
		"comment:rotate:t-2:sha-2:42": {kind: "rotate", taskID: "t-2", headSHA: "sha-2", oldID: 42},
		"comment:rotate:t-2::42":      {kind: "rotate", taskID: "t-2", headSHA: "", oldID: 42},
	}
	for key, want := range valid {
		got, ok := parseCommentKey(key)
		if !ok || got != want {
			t.Errorf("parseCommentKey(%q) = %+v,%v want %+v", key, got, ok, want)
		}
	}
	for _, key := range []string{
		"", "argus:summary:t:s", "comment:unknown:t:s", "comment:summary:t",
		"comment:summary::s", "comment:summary:t:s:extra", "comment:rotate:t:s",
		"comment:rotate:t:s:0", "comment:rotate:t:s:-1", "comment:rotate:t:s:abc",
		"comment:start:t:s",
	} {
		if got, ok := parseCommentKey(key); ok {
			t.Errorf("parseCommentKey(%q) 应拒绝，got %+v", key, got)
		}
	}
}

func TestParsePRResource(t *testing.T) {
	cases := []struct {
		resource, platform, owner, repo, number string
		ok                                      bool
	}{
		{"pr:github:owner/repo#42", "github", "owner", "repo", "42", true},
		{"pr:gitlab:group/sub/repo#9", "gitlab", "group/sub", "repo", "9", true},
		{"", "", "", "", "", false},
		{"pr", "", "", "", "", false},
		{"x:github:o/r#1", "", "", "", "", false},
		{"pr::o/r#1", "", "", "", "", false},
		{"pr:github:o/r", "", "", "", "", false},
		{"pr:github:o/r#", "", "", "", "", false},
		{"pr:github:#1", "", "", "", "", false},
		{"pr:github:repo#1", "", "", "", "", false},
		{"pr:github:owner/#1", "", "", "", "", false},
	}
	for _, c := range cases {
		p, o, r, n, ok := parsePRResource(c.resource)
		if ok != c.ok || p != c.platform || o != c.owner || r != c.repo || n != c.number {
			t.Errorf("parsePRResource(%q) = (%q,%q,%q,%q,%v), want (%q,%q,%q,%q,%v)",
				c.resource, p, o, r, n, ok, c.platform, c.owner, c.repo, c.number, c.ok)
		}
	}
}

func TestCommentResolverSummaryFound(t *testing.T) {
	finder := &fakeCommentFinder{id: 123, found: true}
	res, ok, err := NewCommentResolver(finder).Resolve(context.Background(), storage.UncertainSideEffect{
		Key:      "comment:summary:t-1:sha-1",
		Resource: "pr:github:acme/widgets#17",
	})
	if err != nil || !ok || !res.Applied || res.Reason != "comment marker found" {
		t.Fatalf("Resolve = %+v,%v,%v", res, ok, err)
	}
	var payload commentResolutionPayload
	if err := json.Unmarshal(res.Result.Payload, &payload); err != nil || payload.CommentID != 123 {
		t.Fatalf("结果载荷 = %q, err=%v", res.Result.Payload, err)
	}
	if len(finder.calls) != 1 {
		t.Fatalf("finder 调用次数 = %d", len(finder.calls))
	}
	call := finder.calls[0]
	if call.platform != "github" || call.owner != "acme" || call.repo != "widgets" ||
		call.number != "17" || call.taskID != "t-1" || call.headSHA != "sha-1" {
		t.Errorf("finder 参数错: %+v", call)
	}
}

func TestCommentResolverRotateFoundNestedGroup(t *testing.T) {
	finder := &fakeCommentFinder{id: 9, found: true}
	res, ok, err := NewCommentResolver(finder).Resolve(context.Background(), storage.UncertainSideEffect{
		Key:      "comment:rotate:t-2:sha-2:88",
		Resource: "pr:gitlab:group/sub/repo#3",
	})
	if err != nil || !ok || !res.Applied {
		t.Fatalf("Resolve = %+v,%v,%v", res, ok, err)
	}
	call := finder.calls[0]
	if call.platform != "gitlab" || call.owner != "group/sub" || call.repo != "repo" ||
		call.number != "3" || call.taskID != "t-2" || call.headSHA != "sha-2" {
		t.Errorf("finder 参数错: %+v", call)
	}
}

func TestCommentResolverMarkerAbsentIsRetryable(t *testing.T) {
	finder := &fakeCommentFinder{found: false}
	res, ok, err := NewCommentResolver(finder).Resolve(context.Background(), storage.UncertainSideEffect{
		Key:      "comment:summary:t-3:",
		Resource: "pr:gitea:o/r#1",
	})
	if err != nil || !ok {
		t.Fatalf("Resolve = %+v,%v,%v", res, ok, err)
	}
	if res.Applied || !res.ForceRetryable || res.Reason != "comment marker absent" {
		t.Fatalf("未命中应显式可重试: %+v", res)
	}
}

func TestCommentResolverFinderErrorIsNotSkipped(t *testing.T) {
	boom := errors.New("list failed")
	finder := &fakeCommentFinder{err: boom}
	_, ok, err := NewCommentResolver(finder).Resolve(context.Background(), storage.UncertainSideEffect{
		Key:      "comment:summary:t-4:sha",
		Resource: "pr:github:o/r#1",
	})
	if !ok || !errors.Is(err, boom) {
		t.Fatalf("查询失败应作为错误返回（不可静默跳过）: ok=%v err=%v", ok, err)
	}
}

func TestCommentResolverUnsupportedInputs(t *testing.T) {
	finder := &fakeCommentFinder{id: 1, found: true}
	r := NewCommentResolver(finder)
	cases := []storage.UncertainSideEffect{
		{Key: "branch:push:t-1", Resource: "pr:github:o/r#1"},
		{Key: "comment:summary:t-1:sha", Resource: "repo:o/r#1"},
		{Key: "comment:summary:t-1:sha", Resource: "pr:github:o/r#"},
	}
	for _, u := range cases {
		if _, ok, err := r.Resolve(context.Background(), u); ok || err != nil {
			t.Errorf("Resolve(%+v) 应 ok=false,nil；实得 ok=%v err=%v", u, ok, err)
		}
	}
	if len(finder.calls) != 0 {
		t.Fatalf("不支持的输入不应查询平台: %v", finder.calls)
	}

	if _, ok, err := (*CommentResolver)(nil).Resolve(context.Background(), cases[0]); ok || err != nil {
		t.Fatalf("nil resolver 应跳过: ok=%v err=%v", ok, err)
	}
	if _, ok, err := NewCommentResolver(nil).Resolve(context.Background(), cases[0]); ok || err != nil {
		t.Fatalf("nil finder 应跳过: ok=%v err=%v", ok, err)
	}
}

func TestCommentResolverRejectsInvalidCommentID(t *testing.T) {
	finder := &fakeCommentFinder{id: 0, found: true}
	_, ok, err := NewCommentResolver(finder).Resolve(context.Background(), storage.UncertainSideEffect{
		Key:      "comment:summary:t-5:sha",
		Resource: "pr:github:o/r#1",
	})
	if !ok || err == nil {
		t.Fatalf("命中但 ID 非法应报错: ok=%v err=%v", ok, err)
	}
}
