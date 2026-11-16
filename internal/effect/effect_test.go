// effect_test.go —— #51 验收单测：哨兵区分（ErrUnknownOutcome ≠
// ErrOutcomeUnknown）+ RetryBlocked 对未知结论/未知结果的判定 + 包裹链穿透 +
// 普通存储错误可降级（不禁重试）。
package effect

import (
	"errors"
	"fmt"
	"testing"
)

func TestSentinelsDistinct(t *testing.T) {
	// 与 ErrOutcomeUnknown 区分（issue #51 验收）：互不 errors.Is、文案不同。
	if errors.Is(ErrUnknownOutcome, ErrOutcomeUnknown) {
		t.Fatal("ErrUnknownOutcome 不应被判定为 ErrOutcomeUnknown（哨兵必须可区分）")
	}
	if errors.Is(ErrOutcomeUnknown, ErrUnknownOutcome) {
		t.Fatal("ErrOutcomeUnknown 不应被判定为 ErrUnknownOutcome（哨兵必须可区分）")
	}
	if ErrUnknownOutcome.Error() == ErrOutcomeUnknown.Error() {
		t.Fatal("两个哨兵文案不应相同（排障面要求可分辨哪一侧失明）")
	}
}

func TestRetryBlockedUnknownVerdicts(t *testing.T) {
	plain := errors.New("git push exit 128")
	cases := []struct {
		name    string
		err     error
		blocked bool
	}{
		{"nil", nil, false},
		{"普通存储错误", plain, false},
		{"未知结论哨兵", ErrUnknownOutcome, true},
		{"未知结果哨兵", ErrOutcomeUnknown, true},
		{"未知结论被 wrap", fmt.Errorf("fix push 归属检查: %w: %w", ErrUnknownOutcome, plain), true},
		{"未知结果被 wrap", fmt.Errorf("deep: %w", ErrOutcomeUnknown), true},
	}
	for _, tc := range cases {
		if got := RetryBlocked(tc.err); got != tc.blocked {
			t.Errorf("%s: RetryBlocked = %v, want %v", tc.name, got, tc.blocked)
		}
	}
}

func TestStorageFailureUnknownNonDegrade(t *testing.T) {
	// 未知结论/未知结果一律 non-degrade（issue #51：storageFailure 已把
	// ErrUnknownOutcome 列为 non-degrade，RetryBlocked 必须同口径收编）。
	for _, sentinel := range []error{ErrUnknownOutcome, ErrOutcomeUnknown} {
		cls := StorageFailure(fmt.Errorf("wrap: %w", sentinel))
		if cls.Degrade {
			t.Errorf("%v 被列为可降级，want non-degrade", sentinel)
		}
		if !cls.RetryBlocked {
			t.Errorf("%v 未被判 RetryBlocked", sentinel)
		}
	}
	// 普通存储抖动：可降级、不禁重试（既有重试面继续兜）。
	cls := StorageFailure(errors.New("dial tcp refused"))
	if !cls.Degrade || cls.RetryBlocked {
		t.Errorf("普通存储错误分类 = %+v, want {Degrade:true RetryBlocked:false}", cls)
	}
	if zero := StorageFailure(nil); zero != (Class{}) {
		t.Errorf("nil 失败分类 = %+v, want 零值", zero)
	}
}
