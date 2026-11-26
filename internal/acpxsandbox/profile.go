// Package acpxsandbox 把 acpx CLI 编码 agent 的执行面接到 kernel 的五项契约上：
// backend（任务式执行后端登记）、gatecheck（审批×档位×出口联合判据）、
// preset（具名权限预设）、cmdpolicy（命令行策略）、egress（出站策略）。
//
// # 边界（先说不做什么）
//
// 本包**不**提供容器/VM/网络命名空间：acpx agent 仍是宿主进程拉起的子进程
// （argus 与 oagentkit/tools/acpx 的分工，spawn 在第三方 SDK 内）。故：
//
//   - backend 自报 IsolationProcess，不冒充 IsolationContainer；
//   - 出口只能声明 sandbox.EgressUnrestricted：环境变量是建议，不是强制，
//     要求 proxy/denied 时**拒绝执行**（fail closed），不伪造「已禁网」；
//   - gemini/qwen/... 的 data 目录、SSH agent 等宿主凭据仍可能被子进程按
//     白名单可见——本包只收窄 acpx **自报**的沙箱旗标与出站声明，不是安全边界。
//
// # 三档沙箱模型
//
// readonly / write / network 是**provider-native 文件系统档**（acpx.RunRequest
// 的 Sandbox 字段），不是 kernel 隔离档位：kernel 侧三档都声明 LevelProcess。
// 三档的 kernel 预设用审批模式区分（confirm/auto/full），出口要求共享同一份
// 全局声明（ARGUS_ACPX_EGRESS_*）。
package acpxsandbox

import (
	"fmt"
	"strings"

	"github.com/oagentkit/guardx/sandbox"
	"github.com/oagentkit/toolgate/policy"
	"github.com/oagentkit/tools/acpx"

	"github.com/yi-nology/goagent-kernel/egress"
	"github.com/yi-nology/goagent-kernel/preset"
)

// Profile acpx 执行沙箱档位（provider-native 文件系统档）。
type Profile string

const (
	// ProfileReadonly 只读审查：agent 只能读工作副本（claude→plan /
	// codex→read-only / gemini→default）。
	ProfileReadonly Profile = "readonly"
	// ProfileWrite 修复沙箱：agent 可在独立工作副本里写（workspace-write）。
	ProfileWrite Profile = "write"
	// ProfileNetwork 完全访问：无 provider-native 文件系统沙箱（full）。
	//
	// 它不是「只在需要网络时才用」——CLI agent 调模型 API 本来就要出网；
	// 这一档的真实语义是「文件系统也不设限」，出站仍由 egress 声明约束。
	ProfileNetwork Profile = "network"
)

// ParseProfile 归一沙箱档位；空串取缺省 readonly（审查面最小权限）。
// 未知名报错而非回落到缺省——配置写错时「悄悄用更严的档」看起来安全，
// 但会让部署方以为 write/network 已生效（声明与实际的静默落差）。
func ParseProfile(raw string) (Profile, error) {
	p := Profile(strings.ToLower(strings.TrimSpace(raw)))
	switch p {
	case "":
		return ProfileReadonly, nil
	case ProfileReadonly, ProfileWrite, ProfileNetwork:
		return p, nil
	default:
		return "", fmt.Errorf("acpxsandbox: 未知沙箱档位 %q（readonly|write|network）", raw)
	}
}

// Sandbox 映射到 acpx.RunRequest.Sandbox 词表。
func (p Profile) Sandbox() string {
	switch p {
	case ProfileWrite:
		return acpx.SandboxWorkspace
	case ProfileNetwork:
		return acpx.SandboxFull
	default:
		return acpx.SandboxReadonly
	}
}

// Preset 构造该档位的 kernel 预设。
//
// 三档在 kernel 三轴上只差审批模式：kernel 没有「provider-native 文件系统」
// 这一轴，硬塞进 Level 会说谎（LevelProcess 对三档都是诚实的）。Egress 三档
// 共享同一份部署级声明；文件系统档由 Profile.Sandbox() 单独承载。
//
// 审批模式的边界（如实写明）：本执行入口用 OpExecuteStep，它在 confirm/auto/
// full 三档下一律 auto（只有 plan 模式会拦）。故 mode 在这里的作用是把三档的
// **风险姿态**写进预设并给「三档不可同义」的自检留出区分，不是本入口的强约束；
// 真正的强约束是 provider-native 文件系统档、LevelProcess 下限与 egress 判据。
func (p Profile) Preset(eg egress.Spec) preset.Preset {
	mode := policy.ModeConfirm
	switch p {
	case ProfileWrite:
		mode = policy.ModeAuto
	case ProfileNetwork:
		mode = policy.ModeFull
	}
	return preset.Preset{
		Name:          string(p),
		Level:         sandbox.LevelProcess,
		Mode:          mode,
		Egress:        eg,
		Justification: presetJustification(p),
	}
}

// presetJustification 为什么是这一档（进预设自检与报错文案）。
func presetJustification(p Profile) string {
	switch p {
	case ProfileWrite:
		return "修复沙箱：agent 在独立工作副本内可写（acpx workspace-write），审批按 auto 档"
	case ProfileNetwork:
		return "完全访问档：provider-native 文件系统沙箱关闭（acpx full），审批按 full 档；出站仍受 egress 声明约束"
	default:
		return "只读审查：agent 只能读工作副本（acpx readonly），审批按 confirm 档（最小权限）"
	}
}

// buildPresets 构造三档预设集合并加载期自检。
//
// preset.New 会拒绝「语义相同的两个名字」：三档的 Key 因审批模式不同而互异，
// 故这份集合能通过自检；若未来把三档的 mode 改成同一值，加载期就会报错，
// 而不是让「看起来有三档、实际同一份权限」留在配置里。
func buildPresets(eg egress.Spec) (*preset.Set, error) {
	set, err := preset.New([]preset.Preset{
		ProfileReadonly.Preset(eg),
		ProfileWrite.Preset(eg),
		ProfileNetwork.Preset(eg),
	})
	if err != nil {
		return nil, fmt.Errorf("acpxsandbox: 预设集合自检失败: %w", err)
	}
	return set, nil
}
