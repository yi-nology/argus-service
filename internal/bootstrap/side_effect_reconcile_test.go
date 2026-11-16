// side_effect_reconcile_test.go —— 对账周期解析与启动接线（#43）。
package bootstrap

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	oekitbootstrap "github.com/oekit/bootstrap"
	"github.com/oekit/logx"

	"github.com/yi-nology/argus/internal/config"
	"github.com/yi-nology/argus/internal/effect"
	"github.com/yi-nology/argus/internal/poster"
	"github.com/yi-nology/argus/internal/storage"
)

func TestSideEffectReconcileInterval(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want time.Duration
	}{
		{"unset", "", effect.DefaultReconcileInterval},
		{"explicit", "30s", 30 * time.Second},
		{"hours", "2h", 2 * time.Hour},
		{"zero_disables", "0", 0},
		{"negative_disables", "-1m", -time.Minute},
		{"invalid_falls_back", "not-a-duration", effect.DefaultReconcileInterval},
		{"trimmed", " 45s ", 45 * time.Second},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv(envSideEffectReconcile, c.raw)
			if got := sideEffectReconcileInterval(logx.Nop()); got != c.want {
				t.Fatalf("interval(%q) = %v, want %v", c.raw, got, c.want)
			}
		})
	}
}

func TestStartSideEffectReconcilerRegistersTicker(t *testing.T) {
	t.Setenv(effect.EnvMode, "on")
	t.Setenv(envSideEffectReconcile, "5ms")
	st, err := storage.Open(filepath.Join(t.TempDir(), "reconcile.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	wm := oekitbootstrap.NewWorkerManager(ctx)
	defer wm.Close()

	// Poster 已装配但 SDK 为 nil：本轮无未知态行，不会触达查询面。
	startSideEffectReconciler(wm, st, &System{Poster: &poster.Poster{}}, &config.Config{}, logx.Nop())
	time.Sleep(20 * time.Millisecond) // 让 ticker 至少跑一轮，关停时验证可退出
}

func TestStartSideEffectReconcilerDisabledOrUnwired(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	wm := oekitbootstrap.NewWorkerManager(ctx)
	defer wm.Close()

	t.Setenv(effect.EnvMode, "off")
	startSideEffectReconciler(wm, nil, &System{Poster: &poster.Poster{}}, &config.Config{}, logx.Nop())

	t.Setenv(effect.EnvMode, "on")
	startSideEffectReconciler(wm, nil, nil, &config.Config{}, logx.Nop())
	startSideEffectReconciler(wm, nil, &System{}, &config.Config{}, logx.Nop())

	t.Setenv(envSideEffectReconcile, "0")
	startSideEffectReconciler(wm, nil, &System{Poster: &poster.Poster{}}, &config.Config{}, logx.Nop())
}
