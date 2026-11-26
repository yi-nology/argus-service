// merge_resolver_test.go —— merge 资源串编解码与读回对账判定。
package effect

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/yi-nology/goagent-kernel/sideeffect"
	"github.com/yi-nology/go-git-platform/model"

	"github.com/yi-nology/argus/internal/storage"
)

func TestParseEncodeMergeResource(t *testing.T) {
	cases := []struct {
		in                     string
		plat, owner, repo, num string
		ok                     bool
	}{
		{"merge:gitea:acme/app#7", "gitea", "acme", "app", "7", true},
		{"tenant-a/merge:github:o/r#12", "github", "o", "r", "12", true},
		{"merge:gitlab:group/sub/repo#3", "gitlab", "group", "sub/repo", "3", true},
		{"pr:gitea:acme/app#7", "", "", "", "", false},
		{"merge:bad", "", "", "", "", false},
		{"merge:gitea:acme/app", "", "", "", "", false},
	}
	for _, tc := range cases {
		p, o, r, n, ok := ParseMergeResource(tc.in)
		if ok != tc.ok {
			t.Errorf("%q ok=%v want %v", tc.in, ok, tc.ok)
			continue
		}
		if !ok {
			continue
		}
		if p != tc.plat || o != tc.owner || r != tc.repo || n != tc.num {
			t.Errorf("%q → %s %s/%s #%s", tc.in, p, o, r, n)
		}
	}
	enc := EncodeMergeResource("default", "gitea", "acme", "app", "7")
	if enc != "merge:gitea:acme/app#7" {
		t.Fatalf("default encode: %q", enc)
	}
	enc = EncodeMergeResource("t1", "gitea", "acme", "app", "7")
	if p, o, r, n, ok := ParseMergeResource(enc); !ok || p != "gitea" || o != "acme" || r != "app" || n != "7" {
		t.Fatalf("roundtrip: %q → %s %s/%s #%s ok=%v", enc, p, o, r, n, ok)
	}
}

type mergeFinderStub struct {
	cr  *model.ChangeRequest
	err error
}

func (s mergeFinderStub) GetCR(context.Context, string, string, string, string) (*model.ChangeRequest, error) {
	return s.cr, s.err
}

func mergeU(key, resource string) storage.UncertainSideEffect {
	return storage.UncertainSideEffect{Key: key, Resource: resource}
}

func TestMergeResolverResolve(t *testing.T) {
	key := "merge:m-abc"
	res := EncodeMergeResource("", "gitea", "o", "r", "1")

	// merged → applied
	r := NewMergeResolver(mergeFinderStub{cr: &model.ChangeRequest{
		State: model.CRStateMerged, HeadSHA: "h1",
	}})
	out, ok, err := r.Resolve(context.Background(), mergeU(key, res))
	if err != nil || !ok || !out.Applied {
		t.Fatalf("merged: ok=%v applied=%v err=%v", ok, out.Applied, err)
	}
	var p map[string]any
	if err := json.Unmarshal(out.Result.Payload, &p); err != nil || p["state"] != "merged" {
		t.Fatalf("payload: %s err=%v", out.Result.Payload, err)
	}

	// opened → not applied, force retryable
	r = NewMergeResolver(mergeFinderStub{cr: &model.ChangeRequest{State: model.CRStateOpened}})
	out, ok, err = r.Resolve(context.Background(), mergeU(key, res))
	if err != nil || !ok || out.Applied || !out.ForceRetryable {
		t.Fatalf("opened: ok=%v applied=%v force=%v err=%v", ok, out.Applied, out.ForceRetryable, err)
	}

	// closed → not applied, not force
	r = NewMergeResolver(mergeFinderStub{cr: &model.ChangeRequest{State: model.CRStateClosed}})
	out, ok, err = r.Resolve(context.Background(), mergeU(key, res))
	if err != nil || !ok || out.Applied || out.ForceRetryable {
		t.Fatalf("closed: ok=%v applied=%v force=%v err=%v", ok, out.Applied, out.ForceRetryable, err)
	}

	// 查询失败 → error（认识但裁决不了）
	r = NewMergeResolver(mergeFinderStub{err: errors.New("boom")})
	if _, ok, err := r.Resolve(context.Background(), mergeU(key, res)); err == nil || !ok {
		t.Fatalf("want supported+error, ok=%v err=%v", ok, err)
	}

	// 非 merge key → skip
	r = NewMergeResolver(mergeFinderStub{cr: &model.ChangeRequest{State: model.CRStateMerged}})
	if _, ok, err := r.Resolve(context.Background(), mergeU("comment:xyz", res)); err != nil || ok {
		t.Fatalf("non-merge key should skip: ok=%v err=%v", ok, err)
	}

	// nil finder → skip
	r = NewMergeResolver(nil)
	if _, ok, err := r.Resolve(context.Background(), mergeU(key, res)); err != nil || ok {
		t.Fatalf("nil finder: ok=%v err=%v", ok, err)
	}

	_ = sideeffect.RetrySafetyUnsafe
}
