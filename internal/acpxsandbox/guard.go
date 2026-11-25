// guard.go —— acpx 执行面的装配与执行入口：五项判据在一处收口。
//
// 执行序（顺序即优先级）：
//
//	cmdpolicy（命令行策略）→ egress（出站 env 生成/校验）→
//	preset + gatecheck（审批×档位×出口联合判据）→ provider → kernel backend
//
// 为什么不把 egress 放进 gatecheck 的 Check 里代替：Check 判的是「要求 vs
// 后端能力」，但它不发 env；而 acpx 的 spawn 在第三方 SDK 内，父侧唯一能
// 施加影响的时点是调用 Run 之前——env 必须在**这里**生成并校验，判不过就
// 不调用 agent。
package acpxsandbox

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/oagentkit/guardx/sandbox"
	"github.com/oagentkit/toolgate/policy"
	"github.com/oagentkit/tools/acpx"

	kernelbackend "github.com/oagentkit/goagent-kernel/backend"
	"github.com/oagentkit/goagent-kernel/egress"
	"github.com/oagentkit/goagent-kernel/gatecheck"
	"github.com/oagentkit/goagent-kernel/preset"

	"github.com/yi-nology/argus/internal/execpolicy"
)

// ErrRefused 执行被本包判据拒绝（errors.Is 可判）。
//
// 与「agent 执行失败」区分：前者是**没跑**（判据不放行），后者是**跑了但
// 失败**（退出码/超时）。调用方的处置完全不同——换环境/改配置 vs 重试作业。
var ErrRefused = errors.New("acpxsandbox: 执行被拒绝")

// Config guard 装配参数。
type Config struct {
	// Agent 已注册的 acpx agent（Registry.Get 的产物）。
	Agent acpx.Agent
	// Bin 命令行策略判据用的可执行名（空 = agent.Name()）。
	//
	// 为什么不在本包解析 PATH：真二进制由生产侧（adapter_acpx 的 bins 词表）
	// 提供；本字段只喂 cmdpolicy 的 basename 危险集，空值时用 agent 名兜底。
	Bin string
	// Profile 沙箱档位（空 = readonly）。
	Profile Profile
	// Log 最小日志面（nil = no-op）。
	Log Logger
}

// Request 一次 acpx 执行请求（只保留本仓用到的字段）。
//
// 不直接用 acpx.RunRequest：Sandbox 由 Profile 决定、Env 由 egress 决定，
// 不让调用方有「传一个与判据不一致的 Sandbox/Env 进去」的机会。
type Request struct {
	Prompt  string
	WorkDir string
	Timeout time.Duration
	// Env 调用方额外透传的环境变量（K=V；与 egress env 同名时后者覆盖）。
	Env []string
}

// Guard 一次装配后的 acpx 执行判据组合件（并发安全：内部件只读）。
type Guard struct {
	agent     acpx.Agent
	bin       string
	profile   Profile
	egress    egress.Spec
	presets   *preset.Set
	gate      *policy.Gate
	providers *sandbox.Registry
	log       Logger
}

// nopLogger 空日志面。
type nopLogger struct{}

func (nopLogger) Info(string, ...any) {}
func (nopLogger) Warn(string, ...any) {}

// New 装配 guard：预设集合、审批门、sandbox 登记簿与 kernel backend 登记簿
// 一次构造并自检。任一配置错误在装配期报错（插件停用），不带到运行期。
func New(cfg Config) (*Guard, error) {
	if cfg.Agent == nil {
		return nil, errors.New("acpxsandbox: 未装配 acpx agent")
	}
	profile, err := ParseProfile(string(cfg.Profile))
	if err != nil {
		return nil, err
	}
	s := loadSettings()
	if s.err != nil {
		return nil, fmt.Errorf("acpxsandbox: 出站策略配置非法: %w", s.err)
	}
	presets, err := buildPresets(s.spec)
	if err != nil {
		return nil, err
	}
	backends := &kernelbackend.Registry{}
	if err := backends.Register(NewBackend(cfg.Agent, profile)); err != nil {
		return nil, fmt.Errorf("acpxsandbox: 登记执行后端失败: %w", err)
	}
	provider := newProvider(cfg.Agent, profile, backends)
	providers := sandbox.NewRegistry()
	if err := providers.Register(provider); err != nil {
		return nil, fmt.Errorf("acpxsandbox: 登记执行后端失败: %w", err)
	}
	bin := strings.TrimSpace(cfg.Bin)
	if bin == "" {
		bin = cfg.Agent.Name()
	}
	log := cfg.Log
	if log == nil {
		log = nopLogger{}
	}
	return &Guard{
		agent:     cfg.Agent,
		bin:       bin,
		profile:   profile,
		egress:    s.spec,
		presets:   presets,
		gate:      policy.NewGate(policy.DefaultPolicy(), nil),
		providers: providers,
		log:       log,
	}, nil
}

// Profile 当前沙箱档位。
func (g *Guard) Profile() Profile { return g.profile }

// Egress 当前出站声明（装配时快照）。
func (g *Guard) Egress() egress.Spec { return g.egress }

// Check 只做预设 + 联合判据，不执行（测试与观测面用）。
//
// 判决的 Provider 是下面 Run 真正调用的那一个（同一个对象，不重新 Resolve）。
func (g *Guard) Check(ctx context.Context, req Request) (gatecheck.Decision, error) {
	base := gatecheck.Request{
		Op: policy.Op{
			Type: policy.OpExecuteStep,
			Tool: "acpx:" + g.agent.Name(),
			// acpx 是执行类作业（不是采集面工具调用）：显式声明非变更，
			// 让审批矩阵按 execute_step 判。变更风险在 acpx 内部由
			// provider-native 档位（readonly/write）约束。
			Mutating: policy.Mutating(false),
		},
		Budget:  budgetOf(req.Timeout),
		Program: req.Prompt,
	}
	greq, err := g.presets.GateRequest(base, string(g.profile))
	if err != nil {
		return gatecheck.Decision{}, err
	}
	return gatecheck.Check(ctx, g.gate, g.providers, greq), nil
}

// Run 执行一次 acpx 作业；判据不放行时返回包装 ErrRefused 的错误且**不启动**
// agent（副作用不会发生）。
func (g *Guard) Run(ctx context.Context, req Request) (*acpx.RunResult, error) {
	if strings.TrimSpace(req.Prompt) == "" {
		return nil, fmt.Errorf("%w：prompt 为空", ErrRefused)
	}
	// ① cmdpolicy：命令行策略。acpx 的 argv 在第三方 SDK 内构造，父侧看不见；
	// 这里至少把**可执行名**过一遍同一套前缀规则（deny-shell/dangerous/wrapper）。
	// 完整的命令行判据仍在 execx（唯一 os/exec 出口）。
	if err := execpolicy.CheckArgv([]string{g.bin}); err != nil {
		g.log.Warn("argus.acpxsandbox.cmdpolicy_denied", "bin", g.bin, "error", err.Error())
		return nil, fmt.Errorf("%w：命令行策略拒绝: %w", ErrRefused, err)
	}

	// ② egress：生成并校验出口 env。denied/proxy 的兑现能力由后续 gatecheck
	// 判；这里先闭合「env 必须真的下发」这一半——环境变量不会自己跑到子进程上。
	egressEnv, err := g.egress.Env(nil)
	if err != nil {
		g.log.Warn("argus.acpxsandbox.egress_denied", "mode", g.egress.Mode.String(), "error", err.Error())
		return nil, fmt.Errorf("%w：出站策略拒绝（%s）: %v", ErrRefused, g.egress.Mode, err)
	}
	if verr := g.egress.Verify(envToMap(egressEnv)); verr != nil {
		g.log.Warn("argus.acpxsandbox.egress_not_delivered", "mode", g.egress.Mode.String(), "error", verr.Error())
		return nil, fmt.Errorf("%w：出口 env 未兑现（%s）: %v", ErrRefused, g.egress.Mode, verr)
	}
	env := execpolicy.SanitizeEnv(mergeEnv(req.Env, egressEnv))

	// ③ preset + gatecheck：审批 × 档位 × 出口，任一层不通过即不执行。
	dec, err := g.Check(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("%w：%v", ErrRefused, err)
	}
	if dec.Outcome != gatecheck.OutcomeProceed || dec.Provider == nil {
		g.log.Warn("argus.acpxsandbox.refused", "profile", string(g.profile),
			"outcome", dec.Outcome.String(), "reason", dec.Reason)
		return nil, fmt.Errorf("%w：%s（%s）", ErrRefused, dec.Outcome, dec.Reason)
	}

	// ④ 用**判出来的那个** provider 执行。这里不走 gatecheck.ExecuteAndRun：
	// 它只把 Program/Budget 传下去，丢掉 Request.Dir/Args（kernel v0.41.0 的
	// 已知缺口），而 acpx 的工作目录与出口 env 都在这两个字段里。
	out, err := dec.Provider.Exec(ctx, sandbox.Request{
		Program: req.Prompt,
		Args:    env,
		Dir:     req.WorkDir,
		Budget:  budgetOf(req.Timeout),
	})
	if err != nil {
		return nil, fmt.Errorf("acpx agent %s 执行失败: %w", g.agent.Name(), err)
	}
	if !out.OK {
		// Detail 里是后端原样保留的作业失败 error（超时/取消哨兵仍可 errors.Is）。
		if werr, ok := out.Detail.(error); ok && werr != nil {
			return nil, werr
		}
		msg := "执行失败"
		if out.Err != nil {
			msg = out.Err.Error()
		}
		return nil, fmt.Errorf("acpx agent %s %s", g.agent.Name(), msg)
	}
	if res, ok := out.Detail.(*acpx.RunResult); ok && res != nil {
		return res, nil
	}
	// Detail 不是 acpx 结果（未来后端形态变化）：至少把可判定文本交回调用方。
	return &acpx.RunResult{Text: out.Value}, nil
}

// budgetOf 把 acpx 超时映射成 kernel 预算（只要求墙钟）。
func budgetOf(timeout time.Duration) sandbox.Budget {
	return sandbox.Budget{WallClock: timeout}
}

// envToMap 把 K=V 切片转 lookup 表（无 "=" 的条目忽略：它们不是 env 声明，
// egress.Verify 只看 K=V）。
func envToMap(env []string) map[string]string {
	m := make(map[string]string, len(env))
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok {
			m[k] = v
		}
	}
	return m
}

// mergeEnv base 之上叠加 over；同名键由 over 覆盖（egress 声明必须压过调用方
// 透传值，否则「声明换了、env 还是旧的」）。
func mergeEnv(base, over []string) []string {
	if len(over) == 0 {
		return base
	}
	keys := make(map[string]struct{}, len(over))
	for _, kv := range over {
		if k, _, ok := strings.Cut(kv, "="); ok {
			keys[k] = struct{}{}
		}
	}
	out := make([]string, 0, len(base)+len(over))
	for _, kv := range base {
		if k, _, ok := strings.Cut(kv, "="); ok {
			if _, drop := keys[k]; drop {
				continue
			}
		}
		out = append(out, kv)
	}
	return append(out, over...)
}
