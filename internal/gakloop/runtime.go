// Package gakloop —— 吸收 goagent-kernel v0.50+ 的 agentloop 机制面：
//
//   - worker.Loop 六段驱动（注册→心跳→领取→执行→续期/恢复→优雅退出）
//   - coord.BoardAdapter + MemoryBackend 任务板（令牌/租约/接管代次）
//   - worker.MemRegistry 注册表（同 ID 顶替铸新令牌）
//   - steer 运行中接管（协作中止 vs 用户取消 vs 停机可区分）
//   - checkpoint.SeqGate 阶段产物单调提交
//
// 划界：argus 主任务队列（storage.tasks + oagentkit/queue worker.Pool）仍是
// 审查主链执行体——那是生产钉过的 at-most-once 路径。本包提供**受管子代理
// 执行环**（acpx 沙箱 agent / 修复 agent / 远端 worker），把内核的租约心跳、
// 租约易主取消、优雅排水接过来；未来可平滑替换主队列（Board 适配已就位）。
package gakloop

import (
	"context"
	"fmt"
	"time"

	"github.com/yi-nology/goagent-kernel/coord"
	"github.com/yi-nology/goagent-kernel/taskboard"
	"github.com/yi-nology/goagent-kernel/worker"
)

// Runtime 受管子代理执行环（agentloop 六段生命周期）。
type Runtime struct {
	board    *coord.BoardAdapter
	registry *worker.MemRegistry
	loop     *worker.Loop
	cfg      Config
}

// Config 执行环配置（零值取缺省）。
type Config struct {
	// Agent 认领者标识（缺省 argus-subagent）。
	Agent string
	// TTL 认领租约与注册有效期（缺省 2m；子代理单步短，无需长租约）。
	TTL time.Duration
	// PollInterval 领取轮询（缺省 500ms）。
	PollInterval time.Duration
	// Concurrency 同时在途任务上限（缺省 2）。
	Concurrency int
	// Handler 任务执行体（必填）。
	Handler worker.Handler
}

func (c *Config) withDefaults() error {
	if c.Handler == nil {
		return fmt.Errorf("gakloop: 缺 Handler")
	}
	if c.Agent == "" {
		c.Agent = "argus-subagent"
	}
	if c.TTL <= 0 {
		c.TTL = 2 * time.Minute
	}
	if c.PollInterval <= 0 {
		c.PollInterval = 500 * time.Millisecond
	}
	if c.Concurrency <= 0 {
		c.Concurrency = 2
	}
	return nil
}

// New 装配执行环（内存任务板 + 内存注册表）。单进程语义；跨进程多副本
// 落库换 coord 的持久化 Backend（pgstore/sqliteadapter）即可。
func New(cfg Config) (*Runtime, error) {
	if err := cfg.withDefaults(); err != nil {
		return nil, err
	}
	backend := coord.NewMemoryBackend()
	board, err := coord.NewBoardAdapter(backend)
	if err != nil {
		return nil, fmt.Errorf("gakloop: 任务板装配失败: %w", err)
	}
	reg := worker.NewMemRegistry()
	loop, err := worker.NewLoop(board, reg, cfg.Handler, worker.Config{
		Agent:             taskboard.AgentID(cfg.Agent),
		TTL:               cfg.TTL,
		PollInterval:      cfg.PollInterval,
		Concurrency:       cfg.Concurrency,
		HeartbeatInterval: cfg.TTL / 3,
	})
	if err != nil {
		return nil, fmt.Errorf("gakloop: 驱动装配失败: %w", err)
	}
	return &Runtime{board: board, registry: reg, loop: loop, cfg: cfg}, nil
}

// Board 任务板（播种/查询用）。
func (r *Runtime) Board() taskboard.RenewingBoard { return r.board }

// Registry 注册表（观测用）。
func (r *Runtime) Registry() worker.Registry { return r.registry }

// AddTask 播种一条子代理任务（StateOpen；deps 全部 done 后可认领）。
func (r *Runtime) AddTask(id, title string, deps ...taskboard.TaskID) taskboard.Task {
	t := taskboard.Task{
		ID:    taskboard.TaskID(id),
		Title: title,
		Deps:  deps,
		State: taskboard.StateOpen,
	}
	r.board.Seed(t)
	return t
}

// Run 驱动执行环直到 ctx 取消（优雅退出 = 停止领取 + 在途收敛 + 注销）。
func (r *Runtime) Run(ctx context.Context) error {
	return r.loop.Run(ctx)
}

// Task 查询任务快照（演示/测试用）。
func (r *Runtime) Task(ctx context.Context, id string) (taskboard.Task, bool) {
	tasks, err := r.board.List(ctx)
	if err != nil {
		return taskboard.Task{}, false
	}
	for _, t := range tasks {
		if string(t.ID) == id {
			return t, true
		}
	}
	return taskboard.Task{}, false
}

// WaitDone 轮询直到 n 条任务到 done（测试/同步等待用）。
func (r *Runtime) WaitDone(ctx context.Context, ids []string, within time.Duration) error {
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		allDone := true
		for _, id := range ids {
			t, ok := r.Task(ctx, id)
			if !ok || t.State != taskboard.StateDone {
				allDone = false
				break
			}
		}
		if allDone {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
	return fmt.Errorf("gakloop: 等待 %v 完成超时", ids)
}
