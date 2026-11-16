// cr_resolver_test.go —— 修复 PR 创建型未知态对账（#64）：Key/资源解析回环互钉
// （编码器在 runner crEffectAction/crEffectResource + fixBranchName）、按源分支
// 查既有 PR（不限状态）的两向裁决、判据不足不裁决。
package effect

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/yi-nology/argus/internal/storage"
)

// fakeCRFinder 可编程 PR 查询器。
type fakeCRFinder struct {
	id     int64
	number string
	url    string
	ok     bool
	err    error
	last   string
}

func (f *fakeCRFinder) FindCRBySource(_ context.Context, _, _, _, sourceBranch string) (int64, string, string, bool, error) {
	f.last = sourceBranch
	return f.id, f.number, f.url, f.ok, f.err
}

// encoders 钉住 runner 编码器形态：key=fix:cr:<task>:<head>:branch、
// resource=pr:<platform>:<owner>/<repo>#<number>、源分支=FixBranchPrefix+PR号。
const (
	crKey = "fix:cr:t-1:abc123:branch"
	crRes = "pr:gitea:myorg/app#7"
)

func TestCRResolverFoundAnyState(t *testing.T) {
	f := &fakeCRFinder{id: 42, number: "13", url: "https://git/myorg/app/-/merge_requests/13", ok: true}
	r := NewCRResolver(f)

	res, ok, err := r.Resolve(context.Background(), unc(crKey, crRes))
	if err != nil || !ok {
		t.Fatalf("Resolve = ok=%v err=%v", ok, err)
	}
	// 源分支按命名约定重建：argus/fix/ + 资源串里的 PR 号。
	if f.last != "argus/fix/7" {
		t.Fatalf("探针源分支 = %q, want argus/fix/7（FixBranchPrefix+PR号）", f.last)
	}
	if !res.Applied || res.ForceRetryable || res.Reason != "fix CR exists for source branch" {
		t.Fatalf("裁决不符: %+v", res)
	}
	var p crResolutionPayload
	if jerr := json.Unmarshal(res.Result.Payload, &p); jerr != nil || p.CRID != 42 || p.Number != "13" {
		t.Fatalf("载荷不符: %s %v", res.Result.Payload, jerr)
	}

	// 命中但身份全空 = 数据矛盾，按判据不足处理（不裁决）。
	f2 := &fakeCRFinder{ok: true}
	if _, ok, err := NewCRResolver(f2).Resolve(context.Background(), unc(crKey, crRes)); err == nil || !ok {
		t.Fatalf("空身份命中应报错: ok=%v err=%v", ok, err)
	}
}

func TestCRResolverAbsentSignsRetry(t *testing.T) {
	r := NewCRResolver(&fakeCRFinder{})
	res, ok, err := r.Resolve(context.Background(), unc(crKey, crRes))
	if err != nil || !ok {
		t.Fatalf("Resolve = ok=%v err=%v", ok, err)
	}
	if res.Applied || !res.ForceRetryable {
		t.Fatalf("未生效裁决不符: %+v", res)
	}
	if res.Reason != "fix CR absent for source branch argus/fix/7" {
		t.Fatalf("reason = %q", res.Reason)
	}
	// 平台查询失败 = 判据不足（本轮不裁决）。
	if _, ok, err := NewCRResolver(&fakeCRFinder{err: errors.New("5xx")}).Resolve(context.Background(), unc(crKey, crRes)); err == nil || !ok {
		t.Fatalf("finder error 应透传: ok=%v err=%v", ok, err)
	}
}

func TestCRResolverUnsupportedKeysSkip(t *testing.T) {
	r := NewCRResolver(&fakeCRFinder{})
	for _, u := range []storage.UncertainSideEffect{
		unc("comment:rotate:t-1:abc:5", crRes),           // 他族 Key
		unc("fix:cr:t-1:abc:direct", crRes),              // 未知尾段（PR 创建只在 branch 形态）
		unc(crKey, "branch:gitea:myorg/app:argus/fix/7"), // 资源形态不符
	} {
		if _, ok, err := r.Resolve(context.Background(), u); ok || err != nil {
			t.Errorf("%q 应跳过: ok=%v err=%v", u.Key, ok, err)
		}
	}
	if _, ok, _ := NewCRResolver(nil).Resolve(context.Background(), unc(crKey, crRes)); ok {
		t.Error("nil finder 应跳过")
	}
}

func TestRouterResolverDispatch(t *testing.T) {
	// 路由分发：评论族 Key 落到 CommentResolver（其判据在既有测试），push 族
	// 落到 PushResolver；无人认识 → ok=false。错误立即透传（短路）。
	errProbe := &fakePushProbe{tipErr: errors.New("down")}
	router := NewRouterResolver(NewCommentResolver(nil), NewPushResolver(errProbe), NewCRResolver(&fakeCRFinder{}))

	if _, ok, err := router.Resolve(context.Background(), unc(pushKeyBranch, pushRes)); err == nil || !ok {
		t.Fatalf("push Key 应路由到 PushResolver 并透传错误: ok=%v err=%v", ok, err)
	}
	if _, ok, err := router.Resolve(context.Background(), unc(crKey, crRes)); err != nil || !ok {
		t.Fatalf("cr Key 应路由到 CRResolver: ok=%v err=%v", ok, err)
	}
	if _, ok, err := router.Resolve(context.Background(), unc("totally:unknown", "res")); ok || err != nil {
		t.Fatalf("未知 Key 应全跳过: ok=%v err=%v", ok, err)
	}
	// nil 项忽略；nil resolver 整体安全。
	empty := NewRouterResolver(nil)
	if _, ok, _ := empty.Resolve(context.Background(), unc(pushKeyBranch, pushRes)); ok {
		t.Error("nil 路由应跳过")
	}
}
