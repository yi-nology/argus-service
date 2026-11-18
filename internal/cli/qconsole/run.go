// qconsole —— 审查质量控制台 CLI（P3-02，#27）：
// admin 档 token 拉既有聚合面（/stats/overview · /usage/monthly · /metrics）+
// 评测基线报告文件 → 落静态页（自包含 HTML，离线可开）或 CLI 文本。
// 只读工具：不建表不写业务面；数据纪律见 internal/qconsole 包文档。
//
// 用法：
//
//	go run . qconsole -addr http://argus.internal:8899 -token $ARGUS_ADMIN_TOKEN -days 30 -out console.html
//	go run . qconsole -addr ... -token ... -text            # CLI 文本到 stdout
//	go run . qconsole ... -eval-report eval/results/baseline-latest.json
package qconsole

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/yi-nology/argus/internal/qconsole"
)

func Run(args []string) int {
	fs := flag.NewFlagSet("argus qconsole", flag.ContinueOnError)
	addr := fs.String("addr", "http://127.0.0.1:8899", "argus 服务地址")
	token := fs.String("token", os.Getenv("ARGUS_APITOKEN"), "API token（admin 档；缺省读 ARGUS_APITOKEN）")
	days := fs.Int("days", 30, "统计窗口天数（1..365）")
	out := fs.String("out", "qconsole.html", "静态页输出路径（- = stdout）")
	text := fs.Bool("text", false, "CLI 文本形态（stdout），忽略 -out")
	evalReport := fs.String("eval-report", "eval/results/baseline-latest.json", "评测基线报告路径（P2-02 artifact；不存在则趋势面缺位）")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}

	if *token == "" {
		fmt.Fprintln(os.Stderr, "qconsole: 缺 token（-token 或 ARGUS_APITOKEN；/metrics 与行过滤面需要相应档位）")
		return 2
	}

	src := &qconsole.HTTPSource{BaseURL: *addr, Token: *token}
	snap, err := src.Snapshot(context.Background(), *days)
	if err != nil {
		fmt.Fprintf(os.Stderr, "qconsole: 聚合失败: %v\n", err)
		return 1
	}
	if trend := qconsole.LoadEvalTrend(*evalReport); trend != nil {
		snap.EvalTrend = trend
	}

	if *text {
		fmt.Print(qconsole.RenderText(snap))
		return 0
	}
	html, err := qconsole.RenderHTML(snap)
	if err != nil {
		fmt.Fprintf(os.Stderr, "qconsole: 渲染失败: %v\n", err)
		return 1
	}
	if *out == "-" {
		_, _ = os.Stdout.Write(html)
		return 0
	}
	if err := os.MkdirAll(filepath.Dir(*out), 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "qconsole: %v\n", err)
		return 1
	}
	if err := os.WriteFile(*out, html, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "qconsole: %v\n", err)
		return 1
	}
	fmt.Printf("==> 质量控制台静态页已生成: %s（数据窗口 %d 天）\n", *out, *days)
	return 0
}
