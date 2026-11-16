// procx.go —— 子进程托管纪律的门面。
//
// execx 是 argus 唯一的子进程执行出口（os/exec 唯一直接 import 点）；本文件
// 把 oagentkit/runtime/procx 的类型与入口收编到同一门面，宿主其余包只 import
// internal/execx（architecture-policy.yaml 的 argus-no-direct-procx 保证）。
//
// 这里只有类型别名与薄包装，不改变任何请求参数、超时、截断与错误语义：
// procx 仍是进程组托管（超时/取消整树收场）、环境白名单、限容采集的单一实现。
package execx

import (
	"context"

	"github.com/oagentkit/runtime/procx"
	"github.com/yi-nology/argus/internal/execpolicy"
)

// 类型别名：调用方无需（也不得）直接 import procx。
type (
	// ProcessRequest 独立进程执行请求（procx.RunRequest 的同义类型）。
	ProcessRequest = procx.RunRequest
	// ProcessResult 结构化执行结果（procx.RunResult 的同义类型，含截断事实）。
	ProcessResult = procx.RunResult
)

// 进程托管 sentinel：超时 / 被调用方 context 取消（与 procx 同一 error 值，
// errors.Is 语义不变）。
var (
	// ErrProcessTimeout 运行超时被终止。
	ErrProcessTimeout = procx.ErrTimeout
	// ErrProcessCanceled 运行被调用方 context 取消。
	ErrProcessCanceled = procx.ErrCanceled
)

// ProcessRun 以进程组纪律执行外部命令，返回 stdout/stderr/退出码。
// 薄包装 procx.Run（签名冻结的 tier0 基座）；错误语义原样保留。
//
// 执行前过 execpolicy.CheckArgv（enforce 命中即返回 ErrDenied，不启动进程），
// req.Env 过 execpolicy.SanitizeEnv；其余请求字段原样透传。
func ProcessRun(ctx context.Context, req ProcessRequest) (stdout, stderr string, exitCode int, err error) {
	req, err = prepareRequest(req)
	if err != nil {
		return "", "", -1, err
	}
	return procx.Run(ctx, req)
}

// ProcessRunDetailed 同 ProcessRun，但返回含截断事实的结构化结果。
// 需要 fail-closed 判定「输出是否被采集上限截断」的调用方走这条。
func ProcessRunDetailed(ctx context.Context, req ProcessRequest) (ProcessResult, error) {
	req, err := prepareRequest(req)
	if err != nil {
		return ProcessResult{ExitCode: -1}, err
	}
	return procx.RunDetailed(ctx, req)
}

// prepareRequest 执行策略前置：命令判据 + 环境剥离，其余字段不动。
func prepareRequest(req ProcessRequest) (ProcessRequest, error) {
	if err := execpolicy.CheckArgv(req.Argv); err != nil {
		return req, err
	}
	req.Env = execpolicy.SanitizeEnv(req.Env)
	return req, nil
}
