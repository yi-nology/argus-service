package argusctx

// IsRepoGone 判定面回归：SDK 的 ErrNotFound 哨兵链与 404 status 都要命中；
// 403/5xx/网络错误不命中（权限/故障形态必须保持逐轮可见——轮询三振降噪
// 只吃「仓库不存在」这一类）。

import (
	"errors"
	"fmt"
	"testing"

	"github.com/yi-nology/go-git-platform/provider"
)

func TestIsRepoGone(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"sentinel", provider.ErrNotFound, true},
		{"wrapped sentinel", fmt.Errorf("list: %w", provider.ErrNotFound), true},
		{"provider 404", &provider.ProviderError{Op: "ListCRs", StatusCode: 404}, true},
		{"provider 403", &provider.ProviderError{Op: "ListCRs", StatusCode: 403}, false},
		{"provider 500", &provider.ProviderError{Op: "ListCRs", StatusCode: 500}, false},
		{"plain error", errors.New("dial tcp: timeout"), false},
	}
	for _, c := range cases {
		if got := IsRepoGone(c.err); got != c.want {
			t.Errorf("%s: IsRepoGone = %v, want %v", c.name, got, c.want)
		}
	}
}
