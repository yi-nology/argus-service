// 容器级预算闸（批 C 探针④，#24）：真 PostgreSQL 停机下 soft fail-open /
// hard fail-closed + 重启恢复后正常授予——Phase 1 ARGUS_BUDGET_ENFORCEMENT
// 合同的真库实弹。
//
// 与 storage 探针 db_down_bounded_error_recovery 互补：那边（双方言）证明
// 「真网络故障 = 有界 err 不挂死」（本闸的输入契约），这边证明闸对同一 err
// 的两分支行为在真故障下成立。单测 TestBudgetGateFailOpen/
// HardRejectOnStorageError 用 closed store 覆盖分支逻辑，本文件补真停机
// 形态（docker-proxy 拒连 vs 进程内关库，错误来源不同、err != nil 同分支）。
//
// 门禁同容器级 chaos：ARGUS_CHAOS_CONTAINER=1 才跑（make chaos-container），
// 不进 make test 默认面。budgetGate 未导出故本文件住 runner 包内。
// 方言取 pg 即可：分支判定与方言无关，两方言的网络失败有界性已由 storage
// 探针覆盖——此处省一轮 mysql 拉起。

package runner

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/oekit/logx"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/yi-nology/argus/internal/config"
	"github.com/yi-nology/argus/internal/storage"
)

// TestBudgetGateContainerDown 真库停机三段：soft 放行 → hard 拦截 → 恢复正常。
func TestBudgetGateContainerDown(t *testing.T) {
	if os.Getenv("ARGUS_CHAOS_CONTAINER") != "1" {
		t.Skip("容器级预算闸需 ARGUS_CHAOS_CONTAINER=1（make chaos-container）")
	}
	out, err := exec.Command("docker", "info").CombinedOutput()
	if err != nil {
		t.Fatalf("ARGUS_CHAOS_CONTAINER=1 但 docker 不可用: %v\n%s", err, out)
	}

	ctx := context.Background()
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        "postgres:16-alpine",
			Env:          map[string]string{"POSTGRES_PASSWORD": "test", "POSTGRES_DB": "argus"},
			ExposedPorts: []string{"5432/tcp"},
			WaitingFor: wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).WithStartupTimeout(120 * time.Second),
		},
		Started: true,
	})
	if err != nil {
		t.Fatalf("起 pg 容器: %v", err)
	}
	t.Cleanup(func() { _ = c.Terminate(context.Background()) })

	host, err := c.Host(ctx)
	if err != nil {
		t.Fatalf("host: %v", err)
	}
	mp, err := c.MappedPort(ctx, "5432/tcp")
	if err != nil {
		t.Fatalf("mapped port: %v", err)
	}
	store, err := storage.Open(fmt.Sprintf(
		"postgres://postgres:test@%s:%s/argus?sslmode=disable", host, mp.Port()))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	r := &Runner{
		Cfg:   &config.Config{TaskTokenBudget: 400_000},
		Log:   logx.Nop(),
		Store: store,
	}
	knobs := taskKnobs{monthlyBudget: 500, taskTokenBudget: 400}
	// 三任务停机前落好（hard 拒绝路径要任务行；坐标 gitea/acme/app 与
	// budgetTask 构造一致，预留聚合按此坐标）。
	t1 := budgetTask(t, store, "cg-soft")
	t2 := budgetTask(t, store, "cg-hard")
	t3 := budgetTask(t, store, "cg-recover")

	// 停机（SIGTERM 中断 = 本层故障注入）
	grace := 5 * time.Second
	if err := c.Stop(context.Background(), &grace); err != nil {
		t.Fatalf("停容器: %v", err)
	}

	// soft：判定不可得 → fail-open 放行、授予回落任务预算
	if intercepted, granted := r.budgetGate(ctx, t1, knobs, 0); intercepted {
		t.Error("soft 停机下不应拦截（fail-open）")
	} else if granted != int64(knobs.taskTokenBudget) {
		t.Errorf("soft 授予 = %d, want %d（fail-open 回落任务预算）", granted, knobs.taskTokenBudget)
	}

	// hard：判定不可得 → fail-closed 拦截（rejectByBudget 的 marker/播报在
	// DB 停机 + nil SDK 下 best-effort 有界失败，同 closed-store 单测形态）
	r.Cfg.BudgetEnforcement = "hard"
	if intercepted, granted := r.budgetGate(ctx, t2, knobs, 0); !intercepted {
		t.Error("hard 停机下应拦截（fail-closed）")
	} else if granted != 0 {
		t.Errorf("hard 拦截授予应 0，got %d", granted)
	}
	r.Cfg.BudgetEnforcement = "" // 还原缺省 soft

	// 重启 → **重取端口映射 + 轮询重开 Store**（同 storage 探针实锤：
	// Docker Desktop 随机端口 stop/start 重分配 55028→55029；proxy 先监听
	// pg 后 ready → Open 需重试；失败不覆盖旧引用防 nil Cleanup panic）。
	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("重启容器: %v", err)
	}
	host2, err := c.Host(ctx)
	if err != nil {
		t.Fatalf("重启后取 host: %v", err)
	}
	mp2, err := c.MappedPort(ctx, "5432/tcp")
	if err != nil {
		t.Fatalf("重启后取映射端口: %v", err)
	}
	_ = store.Close() // 旧池已废（Cleanup 再 Close 幂等）

	deadline := time.Now().Add(90 * time.Second)
	var recErr error
	for {
		s, e := storage.Open(fmt.Sprintf(
			"postgres://postgres:test@%s:%s/argus?sslmode=disable", host2, mp2.Port()))
		if e == nil {
			store = s
			r.Store = s // 闸与恢复段共用新池
			recErr = nil
			break
		}
		recErr = e
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if recErr != nil {
		t.Fatalf("恢复后重开 store 仍未成功: %v", recErr)
	}

	// 恢复后闸正常：额度内放行、授予=任务预算
	if intercepted, granted := r.budgetGate(ctx, t3, knobs, 0); intercepted {
		t.Error("恢复后不应拦截")
	} else if granted != int64(knobs.taskTokenBudget) {
		t.Errorf("恢复后授予 = %d, want %d", granted, knobs.taskTokenBudget)
	}
}
