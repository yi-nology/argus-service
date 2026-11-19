package gitdeepen

import (
	"testing"

	"github.com/oagentkit/tools/workcopy"
)

// applyOptions 把可选项折叠进 DeepenOptions（测试断言用）。
func applyOptions(opts []DeepenOption) workcopy.DeepenOptions {
	var o workcopy.DeepenOptions
	for _, fn := range opts {
		if fn != nil {
			fn(&o)
		}
	}
	return o
}

// TestAuthOptionsFailClosed 凭证折叠的 fail-closed 纪律（tools#160/#184）：
// 无 token 空转；token 而期望 clone 源不齐时丢弃 token——旧形态下鉴权 scheme
// 从可被工作树写手篡改的 origin URL 反推，token 外带通道仍开放。
func TestAuthOptionsFailClosed(t *testing.T) {
	if got := (Auth{}).Options(); got != nil {
		t.Fatalf("零值应无鉴权面: %v", got)
	}
	if got := (Auth{Token: "tok"}).Options(); got != nil {
		t.Fatalf("token 而 clone 源缺位应丢弃 token（fail-closed）: %v", got)
	}
	for _, partial := range []Auth{
		{Token: "tok", BaseURL: "https://g.example.com"},
		{Token: "tok", Owner: "o", Repo: "r"},
		{Token: "tok", BaseURL: "https://g.example.com", Owner: "o"},
	} {
		if got := partial.Options(); got != nil {
			t.Fatalf("clone 源不齐应丢弃 token: %+v → %v", partial, got)
		}
	}
}

// TestAuthOptionsComplete 齐备形态注入 token + 期望 clone 源。
func TestAuthOptionsComplete(t *testing.T) {
	a := Auth{Token: "tok", BaseURL: "https://g.example.com", Owner: "obs", Repo: "kylin"}
	opts := a.Options()
	if len(opts) != 2 {
		t.Fatalf("应注入 token + clone 源两个选项: %v", opts)
	}
	got := applyOptions(opts)
	if got.Token != "tok" || got.BaseURL != "https://g.example.com" || got.Owner != "obs" || got.Repo != "kylin" {
		t.Fatalf("注入值不符: %+v", got)
	}
}

// TestAuthOptionsNoTokenWithSource 无 token 但 clone 源齐备：空转（公有远端
// 无鉴权面，不注入空 token）。
func TestAuthOptionsNoTokenWithSource(t *testing.T) {
	a := Auth{BaseURL: "https://g.example.com", Owner: "obs", Repo: "kylin"}
	if got := a.Options(); got != nil {
		t.Fatalf("无 token 应空转: %v", got)
	}
}
