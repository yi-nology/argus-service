// provider.go —— guardx sandbox.Provider 适配：把「判据选中的后端」落到
// kernel backend 登记簿，保证判过的后端与实际跑的后端是同一个。
package acpxsandbox

import (
	"context"
	"strings"

	"github.com/oagentkit/guardx/sandbox"
	"github.com/oagentkit/tools/acpx"

	kernelbackend "github.com/yi-nology/goagent-kernel/backend"
)

// ProviderID 本执行面在 sandbox 登记簿里的标识（**包级常量**：档位登记门禁
// 要静态可识别，见 architecture-policy.yaml 的 sandbox_providers 段）。
const ProviderID = "acpx-subprocess"

// Provider 实现 guardx/sandbox.Provider。
//
// # sandbox.Request 的字段在本后端的语义
//
//   - Program：prompt（acpx agent 的任务描述）；
//   - Args：子进程环境白名单（K=V），名字沿用 kernel 字段而不是另造请求类型
//     ——上游 Request 明确「语义由后端定」；
//   - Dir：agent 工作目录。
//
// 这层映射只在 provider 内解释，Guard 是唯一调用方。
type Provider struct {
	agent    acpx.Agent
	profile  Profile
	backends *kernelbackend.Registry
}

// newProvider 构造 provider（backends 必须已登记本档位的 backend）。
func newProvider(agent acpx.Agent, profile Profile, backends *kernelbackend.Registry) *Provider {
	return &Provider{agent: agent, profile: profile, backends: backends}
}

// ID 执行面标识。
func (*Provider) ID() string { return ProviderID }

// Level 子进程边界：acpx CLI 跑在宿主拉起的子进程里（隔离档位不由 provider-
// native sandbox 决定；readonly/write/network 是文件系统档，不是内核隔离档）。
func (*Provider) Level() sandbox.Level { return sandbox.LevelProcess }

// Egress 显式声明最弱一档（理由见 Backend.Egress）。
func (*Provider) Egress() sandbox.EgressMode { return sandbox.EgressUnrestricted }

// Supports 与 Backend.Capable 同口径：只兜墙钟。
func (*Provider) Supports(b sandbox.Budget) bool {
	return b.MaxMemory <= 0 && b.MaxCalls <= 0 && b.MaxBytes <= 0
}

// Exec 执行一次程序。
//
// 失败分两层：本函数返回 error = 后端基础设施/请求不合法；Outcome.OK=false =
// 作业自己失败（acpx 的退出码/超时/取消）。与 kernel backend.Result 的分层
// 逐字对齐，不在这层重新分类。
func (p *Provider) Exec(ctx context.Context, req sandbox.Request) (sandbox.Outcome, error) {
	spec := kernelbackend.Spec{
		MinIsolation: kernelbackend.IsolationProcess,
		Egress:       sandbox.EgressUnrestricted,
		Budget:       req.Budget,
	}
	backend, err := p.backends.Resolve(spec)
	if err != nil {
		return sandbox.Outcome{}, err
	}
	job := kernelbackend.Job{
		Image:   p.agent.Name(),
		Args:    []string{req.Program},
		Env:     req.Args,
		WorkDir: req.Dir,
		Budget:  req.Budget,
	}
	res, err := backend.Exec(ctx, job)
	if err != nil {
		return sandbox.Outcome{}, err
	}
	out := sandbox.Outcome{OK: res.OK, Value: res.Stdout, Detail: res.Detail}
	if !res.OK {
		msg := strings.TrimSpace(res.Stderr)
		if msg == "" {
			msg = "acpx agent 执行失败"
		}
		out.Err = &sandbox.Failure{Kind: "unknown", Message: msg}
	}
	return out, nil
}
