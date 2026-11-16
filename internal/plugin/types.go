// types.go —— 契约类型面：AgentPlugin/Task/Result/PRContext/DiffContext/
// AgentManifest 与词表（Kind/Protocol 等）（包文档见 doc.go）。

package plugin

import (
	"slices"
)

// Kind 插件形态：builtin 进程内 agent / cli 外部命令行 / remote 远端服务。
type Kind string

const (
	KindBuiltin Kind = "builtin"
	KindCLI     Kind = "cli"
	KindRemote  Kind = "remote"
	KindACPX    Kind = "acpx" // CLI 编码 agent（codex/claude/kimi 等）作为审查专家（oagentkit/tools/acpx 驱动）
)

// Protocol kind=remote 时的协议（v3.0 只留接口位，不做协议子集）。
type Protocol string

const (
	ProtocolA2A Protocol = "a2a"
	ProtocolMCP Protocol = "mcp"
)

// 钉死的技能词表（manifest 声明用）。
const (
	SkillRequirementCompliance = "requirement-compliance"
	SkillCodeReview            = "code-review"
	SkillVisualCheck           = "visual-check"
)

// 钉死的成本词表（预算裁剪按 heavy > standard > fast 顺序）。
const (
	CostFast     = "fast"
	CostStandard = "standard"
	CostHeavy    = "heavy"
)

// ReactRetryHint ReAct agent 失败回喂重试的追加提示（agentrun.Run + WithRetry）。
// 拼在原 query 之后作为重试轮输入；副作用守卫默认生效——首轮已调用变更类工具
// 则不整体重跑，错误如实上抛交调用方降级（review 场景工具表以只读为主，守卫即默认）。
// v3.38.0 起经 ReminderNote 包装（NOT USER INPUT 声明 + 嵌套转义）。
var ReactRetryHint = "\n\n" + ReminderNote("retry-hint",
	"上一次执行因错误中断，请重新完整执行本任务，并只输出最终要求的 JSON。")

// Needs 能力需求清单：新接一个 agent 只需回答"它吃什么（Needs）、吐什么（Result）"。
type Needs struct {
	Diff            bool // 注入 diffprep 清洗后的 diff
	RepoFileRead    bool // 允许通过工具读仓库文件
	WorkingCopy     bool // 需要工作副本沙箱（浅克隆 + checkout PR head）
	RequirementSpec bool // 注入 R1 产出的需求清单
	PreviewURL      bool // 预览地址（前端视觉 agent）
}

var needNames = map[string]func(*Needs){
	"diff":             func(n *Needs) { n.Diff = true },
	"repo-file-read":   func(n *Needs) { n.RepoFileRead = true },
	"working-copy":     func(n *Needs) { n.WorkingCopy = true },
	"requirement-spec": func(n *Needs) { n.RequirementSpec = true },
	"preview-url":      func(n *Needs) { n.PreviewURL = true },
}

// NeedsFromStrings 把 plugins.yaml 的 needs 词表转为 Needs；未知名返回告警列表。
func NeedsFromStrings(names []string) (Needs, []string) {
	var n Needs
	var unknown []string
	for _, name := range names {
		if f, ok := needNames[name]; ok {
			f(&n)
		} else {
			unknown = append(unknown, name)
		}
	}
	return n, unknown
}

// Strings 序列化回词表（GET /agents 可观测用）。
func (n Needs) Strings() []string {
	out := make([]string, 0, 5)
	if n.Diff {
		out = append(out, "diff")
	}
	if n.RepoFileRead {
		out = append(out, "repo-file-read")
	}
	if n.WorkingCopy {
		out = append(out, "working-copy")
	}
	if n.RequirementSpec {
		out = append(out, "requirement-spec")
	}
	if n.PreviewURL {
		out = append(out, "preview-url")
	}
	return out
}

// Trigger 触发条件：路径 glob / 标签 / 事件类型；全空 = 总是触发。
type Trigger struct {
	Paths  []string
	Labels []string
	Events []string // SDK 归一化事件词表：cr.opened / cr.synchronize / cr.synchronized / cr.reopened
}

// Empty 是否为空（空 = 不参与条件触发，默认候选）。
func (t Trigger) Empty() bool { return len(t.Paths) == 0 && len(t.Labels) == 0 && len(t.Events) == 0 }

// Match 判定是否命中（v3.0.1 修订——原实现对"纯 Events 触发器"恒 false，且调用方
// 传空事件类型导致 Events 条件整体失效）。
// 语义：条件类型之间 AND（未声明的类型视为通过），同类型多值之间 OR：
//   - Events 非空：eventType 必须在白名单内；eventType 为空（CLI 手动任务）视为通过；
//   - Paths 非空：任一 glob 命中变更文件即通过；
//   - Labels 非空：任一标签命中即通过；
//   - 全空 = 默认候选。
func (t Trigger) Match(changedFiles, labels []string, eventType string) bool {
	if t.Empty() {
		return true
	}
	if len(t.Events) > 0 && eventType != "" && !slices.Contains(t.Events, eventType) {
		return false
	}
	if len(t.Paths) > 0 && !MatchAnyGlob(t.Paths, changedFiles) {
		return false
	}
	if len(t.Labels) > 0 && !ListOverlap(t.Labels, labels) {
		return false
	}
	return true
}
