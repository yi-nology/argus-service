// side_effects_repoauth_test.go —— #60 控制面仓库维度判定的存储读面：
// 按 Key 取副作用行（裁决前授权）/按资源取租约行（强释前授权）。
package storage

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/yi-nology/goagent-kernel/sideeffect"
)

func newRepoAuthStore(t *testing.T) *SideEffectStore {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "repoauth.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st.SideEffects()
}

// GetUncertainByKey：任意状态的行按 Key 可取（授权判定先于状态判定），
// 不存在返回 ErrNotFound。
func TestGetEffectByKeyForAuthz(t *testing.T) {
	se := newRepoAuthStore(t)
	ctx := context.Background()
	act := sideeffect.Action{Key: "k-authz", Resource: "branch:gitea:myorg/app:argus/fix/42",
		Payload: []byte("p"), RetrySafety: sideeffect.RetrySafetyUnsafe}
	outcome, lease, _, err := se.Acquire(ctx, act, time.Minute)
	if err != nil || outcome != sideeffect.OutcomeApplied {
		t.Fatalf("Acquire outcome=%v err=%v", outcome, err)
	}

	row, err := se.GetEffectByKeyForAuthz(ctx, "k-authz")
	if err != nil {
		t.Fatalf("按 Key 取行: %v", err)
	}
	if row.Resource != act.Resource {
		t.Fatalf("resource = %q, want %q", row.Resource, act.Resource)
	}
	// 已 done 的行同样可取（授权与状态正交，状态冲突由 Resolve 层判）
	if _, err := se.Complete(ctx, act, lease, sideeffect.Result{Payload: []byte("ok")}); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if _, err := se.GetEffectByKeyForAuthz(ctx, "k-authz"); err != nil {
		t.Fatalf("done 行也应可取: %v", err)
	}
	if _, err := se.GetEffectByKeyForAuthz(ctx, "no-such-key"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("不存在应 ErrNotFound, got %v", err)
	}
}

// GetLeaseByResourceForAuthz：按资源取租约行（不要求活跃——授权判定用，
// 释放语义由 ForceReleaseResource 承担），不存在返回 ErrNotFound。
func TestGetLeaseByResourceForAuthz(t *testing.T) {
	se := newRepoAuthStore(t)
	ctx := context.Background()
	act := sideeffect.Action{Key: "k-lease", Resource: "pr:gitea:myorg/app#42",
		Payload: []byte("p"), RetrySafety: sideeffect.RetrySafetyUnsafe}
	outcome, _, _, err := se.Acquire(ctx, act, time.Minute)
	if err != nil || outcome != sideeffect.OutcomeApplied {
		t.Fatalf("Acquire outcome=%v err=%v", outcome, err)
	}

	row, err := se.GetLeaseByResourceForAuthz(ctx, "pr:gitea:myorg/app#42")
	if err != nil {
		t.Fatalf("按资源取租约: %v", err)
	}
	if row.Resource != act.Resource || row.Holder == "" {
		t.Fatalf("租约行 = %+v（holder 应非空）", row)
	}
	if _, err := se.GetLeaseByResourceForAuthz(ctx, "pr:gitea:nope/x#1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("不存在应 ErrNotFound, got %v", err)
	}
}
