// argus-copilot —— Argus 副驾参考客户端（v3.22.0，批次 4 条件项的降档落地）。
//
// 以 MCP 客户端消费 argus /mcp 的副驾工具（get_task_stages / list_findings /
// get_suggestion / ask_argus），为 IDE 插件与脚本集成提供可运行的参考实现：
//
//	argus-copilot -url http://argus:8899/mcp -token $TOKEN stages  -task <id>
//	argus-copilot -url … -token … findings -task <id>
//	argus-copilot -url … -token … suggest -task <id> -fp ab12cd34
//	argus-copilot -url … -token … ask     -pr <platform/owner/repo#num> [-q 问题]
//
// 只读：客户端不改动任何代码——「一键回写」由使用者对 get_suggestion 返回的
// 内容自行执行。GUI 形态（VS Code 插件等）另立项，本 CLI 即集成参考与联调工具。
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/oekit/concurrency/async"
	"github.com/oekit/pkg/cli"
)

// globals 全局连接参数（flag 必须出现在子命令之前，surface 与 v3.124.0 兼容）。
type globals struct {
	url, token string
	timeout    time.Duration
}

// 退出码契约（v3.124.0，review/poll 生效；v5.3.3 起 0/1/2 语义单源 pkg/cli
// 常量，不再手写）：0=通过（无达阈值意见）/异步提交成功；1=有达阈值意见
// （建议阻断）；2=错误（执行失败 cli.ExitError / 用法错误 cli.ExitUsage）；
// 4=空 diff 跳过（exitEmpty，review.go）；130=信号中断。hook 想放行一切照旧
// `|| true`，想阻断就保留退出码。
func main() {
	g := globals{}
	fs := flag.NewFlagSet("argus-copilot", flag.ExitOnError)
	fs.StringVar(&g.url, "url", envOr("ARGUS_MCP_URL", "http://127.0.0.1:8899/mcp"), "argus MCP 端点（ARGUS_MCP_URL）")
	fs.StringVar(&g.token, "token", os.Getenv("ARGUS_APITOKEN"), "Bearer token（ARGUS_APITOKEN）")
	fs.DurationVar(&g.timeout, "timeout", 30*time.Second, "单次调用超时")
	fs.Parse(os.Args[1:])
	rest := fs.Args()

	// 信号中断 → 130（hook/CI 可与「审查发现意见」区分开）。
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	// async.GoSafe（B3）：与 internal 同口径，panic 转留痕不进程死。
	async.GoSafe(func() {
		<-sig
		os.Exit(130)
	})

	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()
	app := &cli.App{Name: "argus-copilot", Commands: commands(g)}
	os.Exit(app.Run(ctx, rest, os.Stdout, os.Stderr))
}

// commands 子命令表（v5.3.3 收编 pkg/cli.App：分发/用法/退出码 0-1-2 单源，
// 每命令独立 FlagSet——假 flag 不再被静默接受而是用法错误）。
func commands(g globals) []cli.Command {
	return append(
		append(queryCommands(g), askCommand(g)),
		append(reviewCommands(g), fixCommand(g))...,
	)
}

// queryCommands 只读查询面：capabilities / stages / findings / suggest。
func queryCommands(g globals) []cli.Command {
	return []cli.Command{
		{
			Name:    "capabilities",
			Summary: "能力面快照：部署开了什么一次看清",
			Run: func(ctx context.Context, args []string, stdout, stderr io.Writer) int {
				c, code := connect(ctx, g, stderr)
				if code != cli.ExitOK {
					return code
				}
				out, err := call(ctx, c, "get_capabilities", map[string]any{})

				return emitCall(stdout, stderr, out, err)
			},
		},
		{
			Name:    "stages",
			Summary: "任务阶段视图（-task 必填）",
			Run: func(ctx context.Context, args []string, stdout, stderr io.Writer) int {
				fs := cli.NewFlagSet("stages", stderr)
				task := fs.String("task", "", "任务 ID（必填）")
				if _, code := cli.ParseFlags(fs, args); code != cli.ExitOK {
					return code
				}
				if *task == "" {
					fmt.Fprintln(stderr, "-task 必填")
					return cli.ExitUsage
				}
				c, code := connect(ctx, g, stderr)
				if code != cli.ExitOK {
					return code
				}
				out, err := call(ctx, c, "get_task_stages", map[string]any{"task_id": *task})

				return emitCall(stdout, stderr, out, err)
			},
		},
		{
			Name:    "findings",
			Summary: "任务意见清单，结构化 JSON（-task 必填）",
			Run: func(ctx context.Context, args []string, stdout, stderr io.Writer) int {
				fs := cli.NewFlagSet("findings", stderr)
				task := fs.String("task", "", "任务 ID（必填）")
				if _, code := cli.ParseFlags(fs, args); code != cli.ExitOK {
					return code
				}
				if *task == "" {
					fmt.Fprintln(stderr, "-task 必填")
					return cli.ExitUsage
				}
				c, code := connect(ctx, g, stderr)
				if code != cli.ExitOK {
					return code
				}
				// list_findings 返回的已是结构化 JSON（fp/severity/suggested_fix），
				// 机器消费无需再包一层信封。
				out, err := call(ctx, c, "list_findings", map[string]any{"task_id": *task})

				return emitCall(stdout, stderr, out, err)
			},
		},
		{
			Name:    "suggest",
			Summary: "单条意见的修复建议（-task 与 -fp 必填）",
			Run: func(ctx context.Context, args []string, stdout, stderr io.Writer) int {
				fs := cli.NewFlagSet("suggest", stderr)
				task := fs.String("task", "", "任务 ID（必填）")
				fp := fs.String("fp", "", "意见指纹短码（必填）")
				if _, code := cli.ParseFlags(fs, args); code != cli.ExitOK {
					return code
				}
				if *task == "" || *fp == "" {
					fmt.Fprintln(stderr, "-task 与 -fp 必填")
					return cli.ExitUsage
				}
				c, code := connect(ctx, g, stderr)
				if code != cli.ExitOK {
					return code
				}
				out, err := call(ctx, c, "get_suggestion", map[string]any{"task_id": *task, "fp": *fp})

				return emitCall(stdout, stderr, out, err)
			},
		},
	}
}

// askCommand PR 问答（v3.3 P5 ask_argus 面）。
func askCommand(g globals) cli.Command {
	return cli.Command{
		Name:    "ask",
		Summary: "PR 问答（-pr platform/owner/repo#number，-q 可选）",
		Run: func(ctx context.Context, args []string, stdout, stderr io.Writer) int {
			fs := cli.NewFlagSet("ask", stderr)
			pr := fs.String("pr", "", "platform/owner/repo#number（必填）")
			q := fs.String("q", "", "追问内容（可选）")
			if _, code := cli.ParseFlags(fs, args); code != cli.ExitOK {
				return code
			}
			if *pr == "" {
				fmt.Fprintln(stderr, "-pr 必填（platform/owner/repo#number）")
				return cli.ExitUsage
			}
			platform, owner, repo, number, err := splitPR(*pr)
			if err != nil {
				fmt.Fprintln(stderr, "argus-copilot:", err)
				return cli.ExitUsage
			}
			c, code := connect(ctx, g, stderr)
			if code != cli.ExitOK {
				return code
			}
			qa := map[string]any{"platform": platform, "owner": owner, "repo": repo, "number": number}
			if *q != "" {
				qa["question"] = *q
			}
			out, err := call(ctx, c, "ask_argus", qa)

			return emitCall(stdout, stderr, out, err)
		},
	}
}

// reviewCommands 本地预审配对面：review（提交）/ poll（轮询到终态）。
// -json/-fail-on 两命令同契约——报告里的意见过阈值即退出 1。
func reviewCommands(g globals) []cli.Command {
	return []cli.Command{
		{
			Name: "review",
			// pre-commit 本地预审（v3.74.1；v3.124.0 起支持 git 侧组装与机器可读面）。
			Summary: "本地 diff 预审：stdin/`git diff --staged` 喂入，-json/-fail-on 定机器面与退出码阈值",
			Run: func(ctx context.Context, args []string, stdout, stderr io.Writer) int {
				fs := cli.NewFlagSet("review", stderr)
				diffFile := fs.String("f", "", "diff 文件路径（缺省读 stdin：git diff --staged | argus-copilot review）")
				staged := fs.Bool("staged", false, "输入源 git diff --staged（与 -f 互斥）")
				uncommitted := fs.Bool("uncommitted", false, "输入源 git diff HEAD（staged+未暂存，与 -f/-staged 互斥）")
				includeUntracked := fs.Bool("include-untracked", false, "追加未跟踪文件为新文件 diff（配合 -staged/-uncommitted）")
				async := fs.Bool("async", false, "异步提交：立即返回 task_id 后退出，配合 poll 轮询结果")
				jsonOut := fs.Bool("json", false, "结构化 JSON 输出（findings/file/line/severity/note）")
				failOn := fs.String("fail-on", "high", "退出码 1 的严重度阈值：high|medium|low|none（none 恒 0）")
				if _, code := cli.ParseFlags(fs, args); code != cli.ExitOK {
					return code
				}
				c, code := connect(ctx, g, stderr)
				if code != cli.ExitOK {
					return code
				}
				out, code, rerr := runReview(ctx, c, reviewInput{
					DiffFile: *diffFile, Staged: *staged, Uncommitted: *uncommitted,
					IncludeUntracked: *includeUntracked, Async: *async,
					JSONOut: *jsonOut, FailOn: *failOn,
				})
				return emit(stdout, stderr, out, code, rerr)
			},
		},
		{
			Name: "poll",
			// 提交方轮询（v3.123.0）：按 task_id 等 diff_preview 任务到终态，打印报告。
			// 自带 10min 上限与 3s 间隔，不吃全局 -timeout（hook/CI 等待场景）。
			// v3.124.0：-json/-fail-on 与 review 同契约——报告里的意见过阈值即退出 1。
			Summary: "按 task_id 轮询异步任务到终态并按意见阈值给退出码（10min 上限，与 review -async 配对）",
			Run: func(ctx context.Context, args []string, stdout, stderr io.Writer) int {
				fs := cli.NewFlagSet("poll", stderr)
				task := fs.String("task", "", "任务 ID（必填）")
				jsonOut := fs.Bool("json", false, "结构化 JSON 输出（与 review -json 同契约）")
				failOn := fs.String("fail-on", "high", "退出码 1 的严重度阈值：high|medium|low|none（none 恒 0）")
				if _, code := cli.ParseFlags(fs, args); code != cli.ExitOK {
					return code
				}
				if *task == "" {
					fmt.Fprintln(stderr, "-task 必填")
					return cli.ExitUsage
				}
				c, code := connect(ctx, g, stderr)
				if code != cli.ExitOK {
					return code
				}
				out, code, rerr := pollTask(context.Background(), c, *task, pollOpts{JSONOut: *jsonOut, FailOn: *failOn})
				return emit(stdout, stderr, out, code, rerr)
			},
		},
	}
}

// fixCommand 沙箱修复（v3.89.0 fix_finding 写侧面）。
func fixCommand(g globals) cli.Command {
	return cli.Command{
		Name:    "fix",
		Summary: "沙箱修复：服务端 agent 出 patch+确定性验证，回显 diff（需 ARGUS_FIX_AGENT，ci+ 档 token）",
		Run: func(ctx context.Context, args []string, stdout, stderr io.Writer) int {
			fs := cli.NewFlagSet("fix", stderr)
			pr := fs.String("pr", "", "platform/owner/repo#number（必填）")
			fp := fs.String("fp", "", "意见指纹短码（必填）")
			if _, code := cli.ParseFlags(fs, args); code != cli.ExitOK {
				return code
			}
			if *pr == "" || *fp == "" {
				fmt.Fprintln(stderr, "-pr 与 -fp 必填")
				return cli.ExitUsage
			}
			platform, owner, repo, number, err := splitPR(*pr)
			if err != nil {
				fmt.Fprintln(stderr, "argus-copilot:", err)
				return cli.ExitUsage
			}
			c, code := connect(ctx, g, stderr)
			if code != cli.ExitOK {
				return code
			}
			out, err := call(ctx, c, "fix_finding", map[string]any{
				"platform": platform, "owner": owner, "repo": repo, "number": number, "fp": *fp,
			})
			return emitCall(stdout, stderr, out, err)
		},
	}
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
