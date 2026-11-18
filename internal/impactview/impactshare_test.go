// impactshare_test.go —— share 令牌词表与租户域（#91）。

package impactview

import "testing"

func TestImpactShareToken(t *testing.T) {
	if got := ImpactShareToken("", "default", "t1"); got != "" {
		t.Fatalf("空 secret 应禁用: %q", got)
	}
	tok := ImpactShareToken("s3cret", "default", "task-1")
	if tok == "" || !ImpactShareValid("s3cret", "default", "task-1", tok) {
		t.Fatalf("default 租户令牌应自校验通过")
	}
	// #91：租户域隔离——同 taskID 不同租户令牌不同；default 存量 legacy 链接
	// 双形态兼容（迁移窗口）。
	ta := ImpactShareToken("s3cret", "tenant-a", "task-1")
	tb := ImpactShareToken("s3cret", "tenant-b", "task-1")
	if ta == tb || ta == tok {
		t.Fatalf("租户域未隔离: default=%q a=%q b=%q", tok, ta, tb)
	}
	if ImpactShareValid("s3cret", "tenant-a", "task-1", tb) {
		t.Fatal("tenant-b 令牌不得通过 tenant-a 校验")
	}
	if !ImpactShareValid("s3cret", "tenant-a", "task-1", ta) {
		t.Fatal("tenant-a 令牌应自校验通过")
	}
	legacy := LegacyShareToken("s3cret", "task-1")
	if !ImpactShareValid("s3cret", "default", "task-1", legacy) {
		t.Fatal("default 租户应兼容 legacy 形态（存量 MR 评论链接）")
	}
	if ImpactShareValid("s3cret", "tenant-a", "task-1", legacy) {
		t.Fatal("legacy 形态不得通过非 default 租户校验")
	}
	if ImpactShareValid("wrong", "default", "task-1", tok) {
		t.Fatal("错误 secret 不得通过")
	}
}
