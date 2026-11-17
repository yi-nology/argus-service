// ptc_sandbox.go —— QA PTC 的执行入口（execution_paths 收口）。
//
// 此前直接调 ptc.Run：它是 kernel 的**裸执行入口**（不经 sandbox 登记簿），
// 档位下限/预算可兑现性/审批门三层全被跳过（architecture-policy.yaml 的
// execution_paths 门禁判的就是这个）。本文件把同一份程序体改成
// sandbox.Execute → Registry.Resolve → ptc.Provider.Exec：档位仍如实声明
// LevelProcess（进程内 goja），预算仍走 ptc.Options 的同名字段，行为不变。
//
// 为什么不用 SubprocessProvider：qa 的工具绑定与 eino 工具实例都在父进程，
// 子进程形态需要 wire 协议与 re-exec 入口；qa 当前选择保持进程内后端，这是
// 显式的档位选择，不是「忘了换更强的后端」。
package qa

import (
	"context"

	"github.com/oagentkit/guardx/sandbox"
	"github.com/yi-nology/goagent-kernel/ptc"
)

// runPTCProgram 经 sandbox 登记簿执行一段 PTC 程序。
//
// 失败不 panic：登记簿装配失败（编程错误）也映射成 ErrorKind "sandbox" 的
// ptc.Result，让 ptcAsk 的既有回退链（回退 ReAct）照常工作——qa 的 PTC 是
// 加速面不是正确性面。
func runPTCProgram(ctx context.Context, program string, bindings []ptc.Binding) ptc.Result {
	reg := sandbox.NewRegistry()
	// AllowUnmanaged 逃逸口与改造前逐字一致（policy 的 exceptions 已登记
	// harness-ptc-unmanaged-escape：qa 工具面在构造期已收口，无动态披露/出站面）。
	if err := reg.Register(ptc.Provider{Bindings: bindings, AllowUnmanaged: true}); err != nil {
		return ptc.Result{OK: false, ErrorKind: "sandbox", Error: err.Error()}
	}
	return executePTC(ctx, reg, program)
}

// executePTC 在已装配的登记簿上执行（测试可注入其它后端形态）。
func executePTC(ctx context.Context, reg *sandbox.Registry, program string) ptc.Result {
	out, err := sandbox.Execute(ctx, reg, sandbox.LevelProcess, sandbox.Request{
		Program: program,
		Budget: sandbox.Budget{
			WallClock: ptcTimeout,
			MaxCalls:  ptcMaxCalls,
			MaxBytes:  ptcMaxBytes,
		},
	})
	if err != nil {
		return ptc.Result{OK: false, ErrorKind: "sandbox", Error: err.Error()}
	}
	if res, ok := out.Detail.(ptc.Result); ok {
		return res
	}
	res := ptc.Result{OK: out.OK, Value: out.Value}
	if !out.OK && out.Err != nil {
		res.Error = out.Err.Error()
		res.ErrorKind = "sandbox"
	}
	return res
}
