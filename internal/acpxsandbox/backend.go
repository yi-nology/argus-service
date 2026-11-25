// backend.go —— kernel 任务式执行后端：acpx CLI agent 作为「镜像 + 参数」作业。
//
// kernel backend 的契约是「按镜像 + 参数跑一个完整作业」。本仓没有容器运行时
// （见 issue #37 的 C2 探针结论），故本后端的 Job.Image 读作 **agent 名**
// （与 kernel Inline 把 Image 读作处理函数名同一条纪律：后端自己解释 Job 的
// 执行语义），Args 是 prompt，WorkDir/Env/Budget 逐字段落到 acpx.RunRequest。
//
// 隔离档位自报 IsolationProcess：作业跑在宿主拉起的子进程里，没有 namespace/
// cgroup/seccomp 边界。把它标成 Container 就是让档位表说谎——档位是宿主做
// 风险判断的依据，说谎的成本落在宿主身上。
package acpxsandbox

import (
	"context"
	"fmt"
	"strings"

	"github.com/oagentkit/guardx/sandbox"
	"github.com/oagentkit/tools/acpx"

	kernelbackend "github.com/oagentkit/goagent-kernel/backend"
)

// Backend 实现 kernel backend.Backend：一个档位一个后端（ID 带档位后缀）。
//
// 为什么按档位分后端而不是一个后端加开关：backend.Registry 的选择判据是
// 隔离档位 × 出站 × 预算，不含 provider-native 文件系统档；把三档塞进一个
// 后端会让「选到了哪个后端」与「实际跑在哪个文件系统档」失去对应关系。
// 本包按登记 id 取回同一档的后端，选择与声明一一对应。
type Backend struct {
	id      string
	agent   acpx.Agent
	profile Profile
}

// NewBackend 构造某档位的 acpx 任务式后端。
func NewBackend(agent acpx.Agent, profile Profile) *Backend {
	return &Backend{id: "acpx-" + string(profile), agent: agent, profile: profile}
}

// BackendID 该档位后端的登记标识（与 ID() 同源，供报告/测试断言）。
func BackendID(profile Profile) string { return "acpx-" + string(profile) }

// ID 后端标识。
func (b *Backend) ID() string { return b.id }

// Isolation 显式声明最弱一档：子进程边界，不是容器/VM。
func (b *Backend) Isolation() kernelbackend.Isolation { return kernelbackend.IsolationProcess }

// Egress 显式声明「没有出站强制面」。
//
// 不是漏实现：实现它并把档位写成 Unrestricted 是一次**签字**——判据里
// proxy/denied 的要求会因「声明了但不够强」被拒（ErrNotSatisfied），而不是
// 因「沉默」被拒（ErrSilent）。两种归因的修法不同：前者要换后端，后者要补
// 声明；本后端两样都做不到，所以如实说「我最弱」。
func (b *Backend) Egress() sandbox.EgressMode { return sandbox.EgressUnrestricted }

// Capable 预算能力：只兑现墙钟（context/RunRequest.Timeout）。
//
// MaxCalls/MaxBytes/MaxMemory 都兜不住：acpx 不逐调用计数、输出上限由 procx
// 写死（不是作业预算）、子进程没有内存限额。照实返回 false，让要求这些项的
// Spec 在 Resolve 阶段就被拒，而不是跑起来再说「好像没兜住」。
func (b *Backend) Capable(budget sandbox.Budget) bool {
	return budget.MaxMemory <= 0 && budget.MaxCalls <= 0 && budget.MaxBytes <= 0
}

// Exec 执行一次作业。
//
// 返回 error = 作业请求本身不合法（后端基础设施层面没有别的故障面：agent
// 进程的失败是作业失败，写在 Result.OK/ExitCode/Stderr 里）。两层失败分开，
// 调用方才能区分「该改作业」与「该换后端」。
func (b *Backend) Exec(ctx context.Context, j kernelbackend.Job) (kernelbackend.Result, error) {
	if err := j.Validate(); err != nil {
		return kernelbackend.Result{}, fmt.Errorf("%w: %v", kernelbackend.ErrBadSpec, err)
	}
	if b.agent == nil {
		return kernelbackend.Result{}, fmt.Errorf("%w: 后端未装配 agent", kernelbackend.ErrBadBackend)
	}
	if j.Image != b.agent.Name() {
		return kernelbackend.Result{}, fmt.Errorf("%w: 作业镜像 %q 与后端 agent %q 不一致",
			kernelbackend.ErrBadSpec, j.Image, b.agent.Name())
	}
	if len(j.Args) != 1 || strings.TrimSpace(j.Args[0]) == "" {
		return kernelbackend.Result{}, fmt.Errorf("%w: acpx 作业必须恰好一个 prompt 参数", kernelbackend.ErrBadSpec)
	}
	if len(j.Mounts) > 0 {
		// 进程后端的挂载不是它自己兑现的：接受挂载声明会让「挂载白名单已判过」
		// 看起来成立，而实际没有任何东西按 Mount.ReadOnly 执行。
		return kernelbackend.Result{}, fmt.Errorf("%w: 进程后端不解释挂载声明（%d 条）",
			kernelbackend.ErrBadSpec, len(j.Mounts))
	}
	res, err := b.agent.Run(ctx, acpx.RunRequest{
		Prompt:  j.Args[0],
		WorkDir: j.WorkDir,
		Sandbox: b.profile.Sandbox(),
		Env:     j.Env,
		Timeout: j.Budget.WallClock,
	})
	if err != nil {
		// 作业失败，不是后端故障：acpx 把超时/取消/非零退出都从这一条通道返回。
		// 原 error 放 Detail，调用方仍可 errors.Is（超时/取消哨兵）。
		return kernelbackend.Result{OK: false, ExitCode: -1, Stderr: err.Error(), Detail: err}, nil
	}
	if res == nil {
		return kernelbackend.Result{OK: false, ExitCode: -1, Stderr: "acpx agent 返回 nil result"}, nil
	}
	return kernelbackend.Result{
		OK:       res.ExitCode == 0,
		ExitCode: res.ExitCode,
		Stdout:   res.Text,
		Stderr:   res.Stderr,
		Detail:   res,
	}, nil
}
