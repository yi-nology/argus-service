// push_resolver_test.go —— push 型未知态对账（#64）：Key/Resource 解析回环互钉
// （编码器在 runner pushEffectAction/pushEffectResource）+ 两形态裁决判据 +
// 判据不足不裁决 + nil probe 全跳过。
package effect

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/yi-nology/argus/internal/storage"
)

// fakePushProbe 可编程探针：按 (platform,owner,repo,branch) 定向返回。
type fakePushProbe struct {
	tip          string
	tipOK        bool
	tipErr       error
	commit       string
	commitOK     bool
	commitErr    error
	lastTaskID   string
	lastBranch   string
	lastPlatform string
}

func (f *fakePushProbe) BranchTipSHA(_ context.Context, platform, owner, repo, branch string) (string, bool, error) {
	if f.tipErr != nil {
		return "", false, f.tipErr
	}
	return f.tip, f.tipOK, nil
}

func (f *fakePushProbe) TaskCommitOnBranch(_ context.Context, platform, owner, repo, branch, taskID string) (string, bool, error) {
	f.lastTaskID, f.lastBranch, f.lastPlatform = taskID, branch, platform
	if f.commitErr != nil {
		return "", false, f.commitErr
	}
	return f.commit, f.commitOK, nil
}

// encoders 钉住 runner 编码器形态（pushEffectAction/pushEffectResource 的回环）：
// key=fix:push:<task>:<head>:<mode>、payload=<key>:<target>、resource=branch:<platform>:<owner>/<repo>:<target>。
const (
	pushKeyBranch = "fix:push:t-1:abc123:branch"
	pushKeyDirect = "fix:push:t-1:abc123:direct"
	pushRes       = "branch:gitea:myorg/app:argus/fix/7"
)

func TestParsePushKey(t *testing.T) {
	cases := []struct {
		key  string
		want pushKey
		ok   bool
	}{
		{pushKeyBranch, pushKey{taskID: "t-1", head: "abc123", mode: "branch"}, true},
		{pushKeyDirect, pushKey{taskID: "t-1", head: "abc123", mode: "direct"}, true},
		{"fix:push:t-1::branch", pushKey{taskID: "t-1", mode: "branch"}, true}, // head 空（回填失败形态）
		{"fix:push:t-1:abc:feature", pushKey{}, false},                         // 未知 mode
		{"fix:push::abc:branch", pushKey{}, false},                             // 空任务 ID
		{"fix:push:t-1:abc", pushKey{}, false},                                 // 段数不足
		{"comment:summary:t-1:abc", pushKey{}, false},                          // 他族 Key
		{"", pushKey{}, false},
	}
	for _, tc := range cases {
		got, ok := parsePushKey(tc.key)
		if ok != tc.ok || got != tc.want {
			t.Errorf("parsePushKey(%q) = %+v %v, want %+v %v", tc.key, got, ok, tc.want, tc.ok)
		}
	}
}

func TestParseBranchResource(t *testing.T) {
	cases := []struct {
		res string
		po  string
		ow  string
		rp  string
		br  string
		ok  bool
	}{
		{pushRes, "gitea", "myorg", "app", "argus/fix/7", true},
		// subgroup 语义与 splitScopedRepo（ParseResourceRepo 单源）一致：subgroup 归 repo 段。
		{"branch:github:myorg/sub/repo:release/1.0", "github", "myorg", "sub/repo", "release/1.0", true},
		{"pr:gitea:myorg/app#7", "", "", "", "", false}, // 他族资源
		{"branch:gitea:myorg/app", "", "", "", "", false},
		{"branch:gitea:myorg/app:", "", "", "", "", false}, // 空分支
		{"", "", "", "", "", false},
	}
	for _, tc := range cases {
		p, o, r, b, ok := ParseBranchResource(tc.res)
		if ok != tc.ok || p != tc.po || o != tc.ow || r != tc.rp || b != tc.br {
			t.Errorf("ParseBranchResource(%q) = (%q,%q,%q,%q,%v)", tc.res, p, o, r, b, ok)
		}
	}
}

func unc(key, resource string) storage.UncertainSideEffect {
	return storage.UncertainSideEffect{Key: key, Resource: resource}
}

func TestPushResolverBranchMode(t *testing.T) {
	probe := &fakePushProbe{tip: "deadbee", tipOK: true}
	r := NewPushResolver(probe)

	// 分支存在 = 已生效：Applied + tip 落载荷。
	res, ok, err := r.Resolve(context.Background(), unc(pushKeyBranch, pushRes))
	if err != nil || !ok {
		t.Fatalf("Resolve = ok=%v err=%v", ok, err)
	}
	if !res.Applied || res.ForceRetryable || res.Reason != "fix branch present on remote" {
		t.Fatalf("裁决不符: %+v", res)
	}
	var p pushResolutionPayload
	if jerr := json.Unmarshal(res.Result.Payload, &p); jerr != nil || p.Branch != "argus/fix/7" || p.Tip != "deadbee" {
		t.Fatalf("载荷不符: %s %s", res.Result.Payload, jerr)
	}

	// 分支不存在（查询成功）= 显式未生效：签字放行重试。
	probe.tipOK = false
	res, ok, err = r.Resolve(context.Background(), unc(pushKeyBranch, pushRes))
	if err != nil || !ok {
		t.Fatalf("Resolve absent = ok=%v err=%v", ok, err)
	}
	if res.Applied || !res.ForceRetryable || res.Reason != "fix branch absent on remote" {
		t.Fatalf("未生效裁决不符: %+v", res)
	}

	// 探测失败 = 判据不足：不裁决，等下一轮。
	probe.tipErr = errors.New("network down")
	if _, ok, err = r.Resolve(context.Background(), unc(pushKeyBranch, pushRes)); err == nil || !ok {
		t.Fatalf("probe error 应透传: ok=%v err=%v", ok, err)
	}
}

func TestPushResolverDirectMode(t *testing.T) {
	probe := &fakePushProbe{commit: "c0ffee", commitOK: true}
	r := NewPushResolver(probe)

	// 命中任务标记提交 = 已生效；判据参数（分支/任务）透传给探针。
	res, ok, err := r.Resolve(context.Background(), unc(pushKeyDirect, pushRes))
	if err != nil || !ok {
		t.Fatalf("Resolve = ok=%v err=%v", ok, err)
	}
	if probe.lastTaskID != "t-1" || probe.lastBranch != "argus/fix/7" || probe.lastPlatform != "gitea" {
		t.Fatalf("探针参数不符: %+v", probe)
	}
	if !res.Applied || res.Reason != "fix commit found on target branch" {
		t.Fatalf("裁决不符: %+v", res)
	}
	var p pushResolutionPayload
	if jerr := json.Unmarshal(res.Result.Payload, &p); jerr != nil || p.Commit != "c0ffee" {
		t.Fatalf("载荷不符: %s", res.Result.Payload)
	}

	// 近期历史无标记 = 显式未生效。
	probe.commitOK = false
	res, ok, err = r.Resolve(context.Background(), unc(pushKeyDirect, pushRes))
	if err != nil || !ok || res.Applied || !res.ForceRetryable {
		t.Fatalf("direct 未生效裁决不符: ok=%v err=%v res=%+v", ok, err, res)
	}

	// 平台检索失败 = 判据不足。
	probe.commitErr = errors.New("api 5xx")
	if _, ok, err = r.Resolve(context.Background(), unc(pushKeyDirect, pushRes)); err == nil || !ok {
		t.Fatalf("probe error 应透传: ok=%v err=%v", ok, err)
	}
}

func TestPushResolverUnsupportedKeysSkip(t *testing.T) {
	r := NewPushResolver(&fakePushProbe{})
	for _, u := range []storage.UncertainSideEffect{
		unc("comment:summary:t-1:abc", "pr:gitea:myorg/app#7"), // 他族 Key
		unc("fix:push:t-1:abc:branch", "pr:gitea:myorg/app#7"), // 资源形态不符
		unc("fix:push:t-1:abc:weird", pushRes),                 // 未知 mode
	} {
		if _, ok, err := r.Resolve(context.Background(), u); ok || err != nil {
			t.Errorf("%q 应跳过: ok=%v err=%v", u.Key, ok, err)
		}
	}
	// nil resolver / nil probe 都不误判。
	if _, ok, err := (*PushResolver)(nil).Resolve(context.Background(), unc(pushKeyBranch, pushRes)); ok || err != nil {
		t.Errorf("nil resolver 应跳过")
	}
	if _, ok, err := NewPushResolver(nil).Resolve(context.Background(), unc(pushKeyBranch, pushRes)); ok || err != nil {
		t.Errorf("nil probe 应跳过")
	}
}
