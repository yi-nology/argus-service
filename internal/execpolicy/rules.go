// rules.go —— 命令名单、cmdpolicy 规则与 shell AST 的 basename 层。
package execpolicy

import (
	"fmt"
	"strings"

	"github.com/oagentkit/tools/cmdpolicy"
	"mvdan.cc/sh/v3/syntax"
)

// dangerousCommands / wrapperCommands 是命令名单的唯一来源：cmdpolicy 规则
// 与宿主侧 basename 集都从这里构造，避免两处名单漂移。mkfs 变体（mkfs.ext4
// 等）在 basename 层按 mkfs* 前缀判定。
var dangerousCommands = []string{
	"rm", "dd", "mkfs", "sudo", "su", "chmod", "chown", "nc", "ncat", "curl", "wget",
}

var wrapperCommands = []string{
	"eval", "source", ".", "command", "exec", "xargs", "env", "find",
	"busybox", "timeout", "nice", "nohup", "setsid", "builtin", "trap",
}

// dangerousBasenames 危险 basename 查找集（路径/大小写/反斜杠归一化后使用）。
var dangerousBasenames = func() map[string]struct{} {
	m := make(map[string]struct{}, len(dangerousCommands)+len(wrapperCommands))
	for _, n := range dangerousCommands {
		m[n] = struct{}{}
	}
	for _, n := range wrapperCommands {
		m[n] = struct{}{}
	}
	return m
}()

// basenameForbidden 构造宿主侧 basename 拒绝裁决。
func basenameForbidden(name string) cmdpolicy.Verdict {
	return cmdpolicy.Verdict{
		Decision:      cmdpolicy.DecisionForbidden,
		Rule:          "deny-basename",
		Justification: fmt.Sprintf("命令名 %q 归一化（路径/大小写/反斜杠）后命中危险 basename 集", name),
		Matched:       1,
	}
}

// innerBasenameVerdict 用 mvdan AST 遍历 `-c` 脚本的每个命令调用，对命令名
// 做与宿主侧相同的 basename 归一化检查。动态命令名（`$CMD` 等）取不到字面，
// 交给 cmdpolicy 的 Dynamic/Prompt 下限处理。
func innerBasenameVerdict(script string) (cmdpolicy.Verdict, error) {
	f, err := syntax.NewParser().Parse(strings.NewReader(script), "shell -c")
	if err != nil {
		return cmdpolicy.Verdict{}, fmt.Errorf("shell -c 脚本 AST 解析失败: %w", err)
	}
	var hit string
	syntax.Walk(f, func(node syntax.Node) bool {
		call, ok := node.(*syntax.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}
		name := call.Args[0].Lit()
		if name == "" {
			return true
		}
		if hit == "" && dangerousBasename(name) {
			hit = name
		}
		return true
	})
	if hit == "" {
		return cmdpolicy.Verdict{Decision: cmdpolicy.DecisionAllow}, nil
	}
	return basenameForbidden(hit), nil
}

// policy 是加载期已自检的前缀规则集（构造后只读，可并发共享）。
var policy = mustPolicy()

// mustPolicy 构造默认规则；规则写错是编程错误，启动期 panic 优于静默降级。
func mustPolicy() *cmdpolicy.Policy {
	p, err := cmdpolicy.New([]cmdpolicy.Rule{
		{
			Name:     "deny-shell",
			Prefix:   cmdpolicy.Prefix{{"sh", "bash", "zsh", "dash", "cmd", "powershell"}},
			Decision: cmdpolicy.DecisionForbidden,
			Justification: "argus 子进程不得经 shell 执行（引号/展开/注入面）；" +
				"请把命令拆成 argv 直传",
			Match:    []string{"sh -c true", "bash -lc true", "zsh -c true", "dash -c true", "cmd /c dir", "powershell -Command x"},
			NotMatch: []string{"git status", "printf hi"},
		},
		{
			Name:     "deny-dangerous",
			Prefix:   cmdpolicy.Prefix{append([]string(nil), dangerousCommands...)},
			Decision: cmdpolicy.DecisionForbidden,
			Justification: "危险命令（破坏性/提权/权限修改/网络外联）不得由 argus 子进程发起；" +
				"mkfs.* 归一为 mkfs 后同判",
			Match: []string{
				"rm -rf /", "dd if=/dev/zero of=/dev/sda", "mkfs /dev/sda1",
				"sudo id", "su root", "chmod 777 x", "chown root x",
				"nc host 80", "ncat host 80", "curl https://x", "wget https://x",
			},
			NotMatch: []string{"git status", "printf hi"},
		},
		{
			Name:     "deny-wrapper",
			Prefix:   cmdpolicy.Prefix{append([]string(nil), wrapperCommands...)},
			Decision: cmdpolicy.DecisionForbidden,
			Justification: "包装器可把危险命令藏进参数/子进程（eval/exec/xargs/env/find -exec …）；" +
				"enforce 一律拒绝。deny-list 不是沙箱，改名二进制/未列包装器仍可能绕过",
			Match: []string{
				"eval rm -rf /", "source ./evil.sh", ". ./evil.sh", "command rm -rf /",
				"exec rm", "xargs rm", "env rm", "find . -exec rm",
				"busybox rm", "timeout 5 rm", "nice rm", "nohup rm", "setsid rm",
				"builtin eval rm -rf /", "trap rm -rf / 0",
			},
			NotMatch: []string{"git status", "printf hi"},
		},
		{
			Name:          "allow-git",
			Prefix:        cmdpolicy.Prefix{{"git"}},
			Decision:      cmdpolicy.DecisionAllow,
			Justification: "argus 的 git 读写/推送链（显式放行，便于报告解释）",
			Match:         []string{"git status", "git push origin main"},
			NotMatch:      []string{"printf hi", "rm -rf /"},
		},
	})
	if err != nil {
		panic("execpolicy: 默认规则自检失败: " + err.Error())
	}
	return p
}
