package chaos_test

// 容器级混沌（#24，批 C）：testcontainers 起真 PostgreSQL/MySQL 跑五探针——
// 单测级矩阵（TestChaosMatrix，sqlite+打桩）覆盖故障语义面，本文件覆盖
// **方言真实行为**（行锁 fencing 复检、唯一索引撞键、FOR UPDATE SKIP LOCKED、
// 方言错误码 isDuplicateKeyErr）与**真基础设施故障注入**（容器停机→有界失败
// →重启→恢复）。
//
// 门禁：ARGUS_CHAOS_CONTAINER=1 才跑（make chaos-container 独立入口，不进
// make test 默认面——本地/普通 CI 无 docker 不红）。env 已开而 docker 不
// 可用 = Fatal 而非 Skip：显式请求必须兑付，防「配了 env 却没 docker」假绿。
// ARGUS_CHAOS_DIALECT=postgres|mysql 可单方言跑（调试用）。
//
// 探针④（budgetGate soft/hard 停机分支）住在 internal/runner
// budget_container_test.go（budgetGate 未导出）；本文件探针5 证明真网络
// 故障=有界 err（闸的输入契约），两文件同由 make chaos-container 拉起。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/yi-nology/argus/internal/storage"
)

// containerProbeResult 单探针结果（报告落盘用；与矩阵报告分名
// chaos-container-latest.*，互不覆盖）。
type containerProbeResult struct {
	Dialect string `json:"dialect"`
	Probe   string `json:"probe"`
	Status  string `json:"status"` // pass | fail
	Error   string `json:"error,omitempty"`
	TookMs  int64  `json:"took_ms"`
}

// requireContainerEnv 容器级门禁：env 未开 Skip（默认测试面保持快）；
// env 开了但 docker 不可用 Fatal（显式请求必须兑付）。
func requireContainerEnv(t *testing.T) {
	t.Helper()
	if os.Getenv("ARGUS_CHAOS_CONTAINER") != "1" {
		t.Skip("容器级混沌需 ARGUS_CHAOS_CONTAINER=1（make chaos-container）")
	}
	out, err := exec.Command("docker", "info").CombinedOutput()
	if err != nil {
		t.Fatalf("ARGUS_CHAOS_CONTAINER=1 但 docker 不可用: %v\n%s", err, out)
	}
}

// dbDialect 方言×镜像×就绪策略×DSN 形态。
type dbDialect struct {
	name  string
	image string
	env   map[string]string
	port  string // 映射端口（容器内），如 "5432/tcp"
	// ready 首次启动就绪（init 阶段日志可用）。重启不做 wait strategy：
	// 日志 occurrence 会命中 Stop 前旧日志产生假就绪——恢复判定一律走
	// 重取端口后的 DB 轮询（真就绪）。
	ready     func() wait.Strategy
	stopGrace time.Duration
	dsn       func(host, port string) string
}

func pgDialect() dbDialect {
	return dbDialect{
		name:  "postgres",
		image: "postgres:16-alpine", // 与 CI test-postgres / 生产同款
		env:   map[string]string{"POSTGRES_PASSWORD": "test", "POSTGRES_DB": "argus"},
		port:  "5432/tcp",
		ready: func() wait.Strategy {
			// entrypoint initdb 临时 server + 正式启动各打一次 ready
			return wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).WithStartupTimeout(120 * time.Second)
		},
		stopGrace: 5 * time.Second,
		dsn: func(h, p string) string {
			return fmt.Sprintf("postgres://postgres:test@%s:%s/argus?sslmode=disable", h, p)
		},
	}
}

func mysqlDialect() dbDialect {
	return dbDialect{
		name:  "mysql",
		image: "mysql:8.0", // 与 CI test-mysql 同款
		env:   map[string]string{"MYSQL_ROOT_PASSWORD": "test", "MYSQL_DATABASE": "argus"},
		port:  "3306/tcp",
		ready: func() wait.Strategy {
			// initdb + 启动各打一次 ready for connections
			return wait.ForLog("ready for connections").
				WithOccurrence(2).WithStartupTimeout(180 * time.Second)
		},
		stopGrace: 15 * time.Second, // InnoDB 关库给足宽限，防 SIGKILL 半写
		dsn: func(h, p string) string {
			return fmt.Sprintf("root:test@tcp(%s:%s)/argus?parseTime=true", h, p)
		},
	}
}

// startDB 拉起方言容器（Started），Cleanup 自动 Terminate。
func startDB(t *testing.T, d dbDialect) (testcontainers.Container, string) {
	t.Helper()
	ctx := context.Background()
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        d.image,
			Env:          d.env,
			ExposedPorts: []string{d.port},
			WaitingFor:   d.ready(),
		},
		Started: true,
	})
	if err != nil {
		t.Fatalf("起 %s 容器（%s）: %v", d.name, d.image, err)
	}
	t.Cleanup(func() { _ = c.Terminate(context.Background()) })

	host, err := c.Host(ctx)
	if err != nil {
		t.Fatalf("%s host: %v", d.name, err)
	}
	mp, err := c.MappedPort(ctx, d.port)
	if err != nil {
		t.Fatalf("%s mapped port: %v", d.name, err)
	}
	return c, d.dsn(host, mp.Port())
}

// openStoreRetry 打开 Store（含 AutoMigrate）带重试：wait strategy 报就绪
// 后仍存在竞态窗（mysql 尾段未稳会 bad connection/EOF；proxy 先监听 pg/mysql
// 后 ready 会 reset），500ms 重试至 60s。迁移幂等，重开安全。
func openStoreRetry(dsn string) (*storage.Store, error) {
	deadline := time.Now().Add(60 * time.Second)
	for {
		s, err := storage.Open(dsn)
		if err == nil {
			return s, nil
		}
		if time.Now().After(deadline) {
			return nil, err
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// openContainerPair 双 Store 实例（双连接池）——并发探针必须跨实例才真并发
// （同实例 withWrite 进程内互斥，测不出数据库层竞争）。
func openContainerPair(t *testing.T, dsn string) (*storage.Store, *storage.Store) {
	t.Helper()
	a, err := openStoreRetry(dsn)
	if err != nil {
		t.Fatalf("open store a: %v", err)
	}
	b, err := openStoreRetry(dsn)
	if err != nil {
		t.Fatalf("open store b: %v", err)
	}
	a.RunnerID = "cont-runner-a"
	b.RunnerID = "cont-runner-b"
	t.Cleanup(func() { _ = a.Close(); _ = b.Close() })
	return a, b
}

// TestContainerChaos 容器级五探针 × 方言（make chaos-container 入口）。
func TestContainerChaos(t *testing.T) {
	requireContainerEnv(t)

	only := os.Getenv("ARGUS_CHAOS_DIALECT") // 调试：单方言过滤
	var results []containerProbeResult

	for _, d := range []dbDialect{pgDialect(), mysqlDialect()} {
		if only != "" && d.name != only {
			continue
		}
		d := d
		t.Run(d.name, func(t *testing.T) {
			c, dsn := startDB(t, d)
			a, b := openContainerPair(t, dsn)

			// 探针顺序固定：db_down 停容器，必须最后。
			probes := []struct {
				name string
				fn   func(t *testing.T) error
			}{
				{"fencing_concurrent_finish", func(t *testing.T) error { return probeFencing(t, a, b) }},
				{"claim_no_double_claim", func(t *testing.T) error { return probeClaim(t, a, b) }},
				{"unique_key_convergence", func(t *testing.T) error { return probeUnique(t, a, b) }},
				{"reserve_no_oversell", func(t *testing.T) error { return probeReserve(t, a, b) }},
				{"db_down_bounded_error_recovery", func(t *testing.T) error {
					return probeDBDown(t, c, d, a)
				}},
			}
			for _, p := range probes {
				start := time.Now()
				err := p.fn(t)
				took := time.Since(start)
				res := containerProbeResult{Dialect: d.name, Probe: p.name, Status: "pass", TookMs: took.Milliseconds()}
				if err != nil {
					res.Status = "fail"
					res.Error = err.Error()
					t.Errorf("%s: %v", p.name, err)
				}
				t.Logf("%s/%s = %s (%dms)", d.name, p.name, res.Status, res.TookMs)
				results = append(results, res)
			}
		})
	}

	writeContainerReport(t, results)
}

// probeFencing 探针①：真行锁下的 fencing——
// (a) 跨执行体：b（异 runner）终态写入必须被 WHERE runner_id 复检拒绝；
// (b) 同执行体并发双写：两连接同 RunnerID 并发 FinishTaskFenced → 恰一赢
// （行锁串行 + 锁后 WHERE status=running 复检；sqlite 单写者下这是 store
// writeLock 的同义反复，真库下才是数据库语义的实弹）。
func probeFencing(t *testing.T, a, b *storage.Store) error {
	ctx := context.Background()

	// (a) 跨执行体 fencing
	task := mkWebhookTask("ev-fence-1", "sha-fence-1")
	dup, _, err := a.CreateTaskForWebhook(ctx, task)
	if err != nil || dup != "" {
		return fmt.Errorf("建任务 dup=%q err=%v", dup, err)
	}
	claimed, ok, err := a.ClaimNextPending(ctx)
	if err != nil || !ok || claimed == nil {
		return fmt.Errorf("a 认领失败 ok=%v err=%v", ok, err)
	}
	fenced, err := b.FinishTaskFenced(ctx, claimed.ID, storage.StatusDone, "approve", "r", "{}", "", 1, 0)
	if err != nil {
		return fmt.Errorf("b 跨执行体终态写入报错: %v", err)
	}
	if !fenced {
		return errors.New("b 跨执行体写入未被 fencing 拒（真行锁下 WHERE runner_id 不匹配应 RowsAffected=0）")
	}
	fenced, err = a.FinishTaskFenced(ctx, claimed.ID, storage.StatusDone, "approve", "r", "{}", "", 1, 0)
	if err != nil || fenced {
		return fmt.Errorf("原主终态写入 fenced=%v err=%v（应成功）", fenced, err)
	}

	// (b) 同执行体并发双写 → 恰一赢
	task2 := mkWebhookTask("ev-fence-2", "sha-fence-2")
	dup, _, err = a.CreateTaskForWebhook(ctx, task2)
	if err != nil || dup != "" {
		return fmt.Errorf("建任务2 dup=%q err=%v", dup, err)
	}
	claimed2, ok, err := a.ClaimNextPending(ctx)
	if err != nil || !ok || claimed2 == nil {
		return fmt.Errorf("a 认领2 失败 ok=%v err=%v", ok, err)
	}
	origB := b.RunnerID
	b.RunnerID = a.RunnerID // 同执行体
	defer func() { b.RunnerID = origB }()

	var (
		mu   sync.Mutex
		wins int
		errs []error
		wg   sync.WaitGroup
	)
	for _, s := range []*storage.Store{a, b} {
		s := s
		wg.Add(1)
		go func() {
			defer wg.Done()
			f, err := s.FinishTaskFenced(ctx, claimed2.ID, storage.StatusDone, "approve", "r", "{}", "", 1, 0)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, err)
				return
			}
			if !f {
				wins++
			}
		}()
	}
	wg.Wait()
	if len(errs) > 0 {
		return fmt.Errorf("并发终态写入报错: %v", errs)
	}
	if wins != 1 {
		return fmt.Errorf("并发同执行体双写赢家 = %d，应恰 1（行锁串行 + status 复检）", wins)
	}
	return nil
}

// probeClaim 探针②：双认领体并发清空队列——FOR UPDATE SKIP LOCKED 真语义：
// 每任务恰被认领一次，零双抢零遗漏。
func probeClaim(t *testing.T, a, b *storage.Store) error {
	ctx := context.Background()
	const n = 20
	for i := 0; i < n; i++ {
		task := mkWebhookTask(fmt.Sprintf("ev-claim-%d", i), fmt.Sprintf("sha-claim-%d", i))
		dup, _, err := a.CreateTaskForWebhook(ctx, task)
		if err != nil || dup != "" {
			return fmt.Errorf("建任务 %d dup=%q err=%v", i, dup, err)
		}
	}

	var (
		mu      sync.Mutex
		seen    = map[string]int{}
		ownedBy = map[string]*storage.Store{} // 认领归属（收尾清理用）
		errs    []error
		wg      sync.WaitGroup
	)
	for _, s := range []*storage.Store{a, b} {
		s := s
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				task, ok, err := s.ClaimNextPending(ctx)
				if err != nil {
					mu.Lock()
					errs = append(errs, err)
					mu.Unlock()
					return
				}
				if !ok || task == nil {
					return // 队列空（ErrRecordNotFound→(nil,false,nil)）
				}
				mu.Lock()
				seen[task.ID]++
				if _, dup := ownedBy[task.ID]; !dup {
					ownedBy[task.ID] = s
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if len(errs) > 0 {
		return fmt.Errorf("认领跑空/报错: %v", errs)
	}
	if len(seen) != n {
		return fmt.Errorf("认领覆盖 %d/%d（缺 %d）", len(seen), n, n-len(seen))
	}
	for id, c := range seen {
		if c != 1 {
			return fmt.Errorf("任务 %s 被认领 %d 次（应恰 1，SKIP LOCKED 防双抢）", id, c)
		}
	}

	// 收尾自清：认领出的 running 由归属 store 终态——不给后续探针留
	// stale running 残留（探针⑤ ResetRunningToPending 断言要恰 1）。
	for id, s := range ownedBy {
		fenced, err := s.FinishTaskFenced(ctx, id, storage.StatusDone, "approve", "r", "{}", "", 1, 0)
		if err != nil || fenced {
			return fmt.Errorf("收尾终态 %s fenced=%v err=%v", id, fenced, err)
		}
	}
	return nil
}

// probeUnique 探针③：唯一索引撞键收敛——
// (a) event_id：双连接并发同键建任务 → 恰一行、恰一创建者；
// (b) invocation_id：双连接并发同键落账 → 双方 nil、恰一行
// （isDuplicateKeyErr 对方言真错误码 pg 23505 / mysql 1062 的实弹）。
func probeUnique(t *testing.T, a, b *storage.Store) error {
	ctx := context.Background()

	// (a) event_id
	const ev = "ev-unique-1"
	var (
		mu   sync.Mutex
		dups []string
		errs []error
		wg   sync.WaitGroup
	)
	for _, s := range []*storage.Store{a, b} {
		s := s
		wg.Add(1)
		go func() {
			defer wg.Done()
			dup, _, err := s.CreateTaskForWebhook(ctx, mkWebhookTask(ev, "sha-unique-1"))
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, err)
				return
			}
			dups = append(dups, dup)
		}()
	}
	wg.Wait()
	if len(errs) > 0 {
		return fmt.Errorf("并发建任务报错: %v", errs)
	}
	creators := 0
	for _, d := range dups {
		if d == "" {
			creators++
		}
	}
	if creators != 1 {
		return fmt.Errorf("创建者 = %d/%d（应恰 1，唯一索引收敛）", creators, len(dups))
	}
	tasks, err := a.ListTasksByHead(ctx, "gitea", "acme", "chaos", "42", "sha-unique-1")
	if err != nil {
		return fmt.Errorf("查任务行: %v", err)
	}
	if len(tasks) != 1 {
		return fmt.Errorf("event_id 行数 = %d（应 1）", len(tasks))
	}

	// (b) invocation_id
	taskID := tasks[0].ID
	var invErrs []error
	for _, s := range []*storage.Store{a, b} {
		s := s
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := s.RecordUsageEvent(ctx, &storage.UsageEvent{
				TaskID: taskID, AttemptID: "0", InvocationID: "u-unique-1",
				Platform: "gitea", Owner: "acme", Repo: "chaos",
				Provider: "openai", Model: "test",
				InputTokens: 1, OutputTokens: 1, Status: "settled",
			})
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				invErrs = append(invErrs, err)
			}
		}()
	}
	wg.Wait()
	if len(invErrs) > 0 {
		return fmt.Errorf("并发落账报错（撞键应幂等收敛为 nil，报错=isDuplicateKeyErr 未识别方言码）: %v", invErrs)
	}
	events, err := a.TaskUsageEvents(ctx, taskID)
	if err != nil {
		return fmt.Errorf("查落账行: %v", err)
	}
	if len(events) != 1 {
		return fmt.Errorf("usage_events 行数 = %d（应 1）", len(events))
	}

	// 收尾自清：pending 残留会混入探针⑤的复位后重认领队列——认领即终态。
	claimed3, ok, err := a.ClaimNextPending(ctx)
	if err != nil || !ok || claimed3 == nil || claimed3.ID != taskID {
		return fmt.Errorf("收尾认领失败 ok=%v err=%v", ok, err)
	}
	if fenced, err := a.FinishTaskFenced(ctx, taskID, storage.StatusDone, "approve", "r", "{}", "", 1, 0); err != nil || fenced {
		return fmt.Errorf("收尾终态 fenced=%v err=%v", fenced, err)
	}
	return nil
}

// probeReserve 探针④：预算预留并发原子性——同仓 4 并发（双连接）×任务预算
// 400 vs 配额 1000：预留租约串行化下授予和 ≤1000（零超卖）、首任务足额、
// 至少一拒。
func probeReserve(t *testing.T, a, b *storage.Store) error {
	ctx := context.Background()
	since := time.Now().UTC().Truncate(24 * time.Hour)
	const budget, taskBudget = 1000, 400

	var (
		mu       sync.Mutex
		granted  int64
		rejected int
		errs     []error
		wg       sync.WaitGroup
	)
	stores := []*storage.Store{a, b, a, b}
	for i, s := range stores {
		i, s := i, s
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := s.ReserveRepoBudget(ctx, storage.ReserveInput{
				TaskID: fmt.Sprintf("resv-task-%d", i), AttemptID: "0",
				Platform: "gitea", Owner: "acme", Repo: "chaos",
				TaskBudget: taskBudget, QuotaBudget: budget, Since: since,
			})
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, err)
				return
			}
			switch res.Verdict {
			case storage.ReserveOK:
				granted += res.GrantedTokens
			case storage.ReserveQuotaExceeded, storage.ReserveRealExceeded:
				rejected++
			default:
				errs = append(errs, fmt.Errorf("未知 verdict %v", res.Verdict))
			}
		}()
	}
	wg.Wait()
	if len(errs) > 0 {
		return fmt.Errorf("并发预留报错: %v", errs)
	}
	if granted > budget {
		return fmt.Errorf("超卖：授予和 %d > 配额 %d", granted, budget)
	}
	if granted < taskBudget {
		return fmt.Errorf("授予和 %d 过低（首任务应足额 %d）", granted, taskBudget)
	}
	if rejected == 0 {
		return errors.New("无拒绝（4×400 > 1000 应至少 1 拒）")
	}
	return nil
}

// probeDBDown 探针⑤：真基础设施中断与恢复——
// 停容器 → 写/判定有界失败（ctx 5s，不许挂死）→ 重启 → 轮询恢复 →
// ResetRunningToPending 复位 + 重认领。budgetGate 对有界 err 的 soft/hard
// 分支由 runner budget_container_test.go 覆盖（budgetGate 未导出）。
func probeDBDown(t *testing.T, c testcontainers.Container, d dbDialect, store *storage.Store) error {
	ctx := context.Background()
	since := time.Now().UTC().Truncate(24 * time.Hour)

	// 就绪基线 + 中断任务（running 态供复位探针）
	task := mkWebhookTask("ev-down-1", "sha-down-1")
	dup, _, err := store.CreateTaskForWebhook(ctx, task)
	if err != nil || dup != "" {
		return fmt.Errorf("建任务 dup=%q err=%v", dup, err)
	}
	claimed, ok, err := store.ClaimNextPending(ctx)
	if err != nil || !ok || claimed == nil {
		return fmt.Errorf("认领失败 ok=%v err=%v", ok, err)
	}
	if _, err := store.ReserveRepoBudget(ctx, storage.ReserveInput{
		TaskID: "down-baseline", AttemptID: "0",
		Platform: "gitea", Owner: "acme", Repo: "chaos",
		TaskBudget: 400, QuotaBudget: 1000, Since: since,
	}); err != nil {
		return fmt.Errorf("停机前预留基线失败: %v", err)
	}

	// 停容器（Container 接口无 Pause——Stop=SIGTERM 中断即本层故障注入）
	grace := d.stopGrace
	if err := c.Stop(context.Background(), &grace); err != nil {
		return fmt.Errorf("停容器: %v", err)
	}
	// 保证心跳停摆超过 stale 窗（复位探针的前置）
	time.Sleep(chaosStaleAfter + 50*time.Millisecond)

	// 中断写：有界失败（5s ctx 上限；实际应秒内拒连）
	start := time.Now()
	wctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	_, ferr := store.FinishTaskFenced(wctx, claimed.ID, storage.StatusDone, "approve", "r", "{}", "", 1, 0)
	cancel()
	if ferr == nil {
		return errors.New("DB 停机下终态写入应报错（有界失败）")
	}
	if el := time.Since(start); el > 6*time.Second {
		return fmt.Errorf("终态写入失败耗时 %v（应 ≤5s ctx 有界）", el)
	}

	// 中断判定：预留有界报错（budgetGate 输入契约——真网络故障同样落在
	// err != nil 分支且不挂死）
	start = time.Now()
	rctx, cancel2 := context.WithTimeout(ctx, 5*time.Second)
	_, rerr := store.ReserveRepoBudget(rctx, storage.ReserveInput{
		TaskID: "down-interrupted", AttemptID: "0",
		Platform: "gitea", Owner: "acme", Repo: "chaos",
		TaskBudget: 400, QuotaBudget: 1000, Since: since,
	})
	cancel2()
	if rerr == nil {
		return errors.New("DB 停机下预留应报错")
	}
	if el := time.Since(start); el > 6*time.Second {
		return fmt.Errorf("预留失败耗时 %v（应 ≤5s ctx 有界）", el)
	}

	// 重启 → **重取端口映射 + 轮询重开 Store**。
	// 关键实锤（2026-11-15 本机复现）：①Docker Desktop 对随机宿主端口
	//（-p 0）stop/start **重新分配宿主端口**（55028→55029），缓存 DSN 连
	// 旧端口必永久拒连——Linux 不漂移，重取=同端口，两边统一重取；
	// ②Start 后 proxy 先监听、pg 后 ready（Open 会遇 connection reset，
	// 必须进重试轮询）；③重启 wait strategy 命中 Stop 前旧日志（假就绪），
	// 恢复判定只认 Open 成功（内含 AutoMigrate 真写 = DB 真就绪）。
	// 旧池连接全 stale，Open 成功后才 Close 旧引用——失败不覆盖（防 nil
	// Cleanup panic）。
	if err := c.Start(context.Background()); err != nil {
		return fmt.Errorf("重启容器: %v", err)
	}
	host, err := c.Host(ctx)
	if err != nil {
		return fmt.Errorf("重启后取 host: %v", err)
	}
	mp, err := c.MappedPort(ctx, d.port)
	if err != nil {
		return fmt.Errorf("重启后取映射端口: %v", err)
	}
	_ = store.Close() // 旧池已废（Cleanup 会再 Close，database/sql Close 幂等）

	// 重开走重试（恢复窗同样有就绪竞态；mysql InnoDB 恢复可达数十秒——
	// helper 60s deadline 不够时此处包一层到 90s）
	var recErr error
	deadline := time.Now().Add(90 * time.Second)
	for {
		s, e := openStoreRetry(d.dsn(host, mp.Port()))
		if e == nil {
			store = s
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
		return fmt.Errorf("恢复后重开 store 仍未成功: %v", recErr)
	}

	// 任务复位恢复：停机耗时 >> staleAfter → running 该复位回 pending
	n, err := store.ResetRunningToPending(ctx, chaosStaleAfter)
	if err != nil {
		return fmt.Errorf("复位: %v", err)
	}
	if n != 1 {
		return fmt.Errorf("复位行数 = %d（应 1：中断任务回 pending）", n)
	}
	t2, ok, err := store.ClaimNextPending(ctx)
	if err != nil || !ok || t2 == nil || t2.ID != claimed.ID {
		got := "nil"
		if t2 != nil {
			got = t2.ID
		}
		return fmt.Errorf("复位后重认领失败 ok=%v id=%s want=%s err=%v", ok, got, claimed.ID, err)
	}
	return nil
}

// writeContainerReport 报告落 results/chaos-container-latest.{json,md}
// （与矩阵报告 chaos-latest.* 分名；results/ 已 gitignore，CI artifact 面）。
func writeContainerReport(t *testing.T, results []containerProbeResult) {
	t.Helper()
	if len(results) == 0 {
		return // 单方言过滤下另一方言零探针
	}
	dir := "results"
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("建报告目录: %v", err)
	}

	jsonPath := filepath.Join(dir, "chaos-container-latest.json")
	f, err := os.Create(jsonPath)
	if err != nil {
		t.Fatalf("写报告 json: %v", err)
	}
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	werr := enc.Encode(map[string]any{
		"generated_at": time.Now().UTC().Format(time.RFC3339),
		"probes":       results,
	})
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		t.Fatalf("落盘 json: %v", werr)
	}

	mdPath := filepath.Join(dir, "chaos-container-latest.md")
	var md []byte
	md = append(md, []byte("# 容器级混沌报告（#24 批 C）\n\n")...)
	md = append(md, []byte(fmt.Sprintf("- 生成：%s\n- 入口：make chaos-container（ARGUS_CHAOS_CONTAINER=1）\n\n",
		time.Now().UTC().Format(time.RFC3339)))...)
	md = append(md, []byte("| 方言 | 探针 | 状态 | 耗时(ms) |\n|---|---|---|---|\n")...)
	pass := 0
	for _, r := range results {
		if r.Status == "pass" {
			pass++
		}
		md = append(md, []byte(fmt.Sprintf("| %s | %s | %s | %d |\n", r.Dialect, r.Probe, r.Status, r.TookMs))...)
	}
	md = append(md, []byte(fmt.Sprintf("\n合计 %d/%d pass。\n", pass, len(results)))...)
	if err := os.WriteFile(mdPath, md, 0o644); err != nil {
		t.Fatalf("落盘 md: %v", err)
	}
	t.Logf("报告已写 %s / %s（%d/%d pass）", jsonPath, mdPath, pass, len(results))
}
