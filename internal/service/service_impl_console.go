// service_impl_console.go —— Service 方法体的包级实现（转发壳见原文件）。

package service

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/yi-nology/argus/internal/obs"
	"github.com/yi-nology/argus/internal/qconsole"
)

// qualityConsoleHTML 的包级实现（转发壳见原文件）。
func qualityConsoleHTML(s *Service, ctx context.Context) (string, int) {
	stats, st := s.StatsOverview(ctx, "", "", "", 30)
	if st != http.StatusOK {
		return "", st
	}
	usage, _ := s.UsageMonthly(ctx) // 失败降级为空块（控制台不因用量面故障 500）
	metrics := obs.ConsoleMetrics()

	var b strings.Builder
	b.WriteString(`<!doctype html><html lang="zh-CN"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>Argus 质量控制台</title><style>
:root{color-scheme:light dark;--bg:#0d1117;--card:#161b22;--fg:#e6edf3;--dim:#8b949e;--line:#30363d;--acc:#58a6ff;--ok:#3fb950;--bad:#f85149;--warn:#d29922}
@media(prefers-color-scheme:light){:root{--bg:#f6f8fa;--card:#fff;--fg:#1f2328;--dim:#656d76;--line:#d0d7de;--acc:#0969da;--ok:#1a7f37;--bad:#cf222e;--warn:#9a6700}}
*{box-sizing:border-box}body{margin:0;padding:24px;background:var(--bg);color:var(--fg);font:14px/1.6 -apple-system,BlinkMacSystemFont,"Segoe UI",Helvetica,Arial,sans-serif}
h1{font-size:20px;margin:0 0 4px}h2{font-size:15px;margin:24px 0 8px;color:var(--acc)}
.sub{color:var(--dim);font-size:12px;margin-bottom:20px}
.grid{display:grid;grid-template-columns:repeat(auto-fill,minmax(180px,1fr));gap:12px}
.card{background:var(--card);border:1px solid var(--line);border-radius:8px;padding:14px 16px}
.card .k{color:var(--dim);font-size:12px}.card .v{font-size:24px;font-weight:600;margin-top:2px}
.card .v.ok{color:var(--ok)}.card .v.bad{color:var(--bad)}.card .v.warn{color:var(--warn)}
table{border-collapse:collapse;width:100%;background:var(--card);border:1px solid var(--line);border-radius:8px;overflow:hidden}
th,td{padding:7px 12px;text-align:left;border-bottom:1px solid var(--line);font-size:13px}
th{color:var(--dim);font-weight:600;background:transparent}tr:last-child td{border-bottom:none}
td.num,th.num{text-align:right;font-variant-numeric:tabular-nums}
.bar{display:inline-block;height:8px;border-radius:4px;background:var(--acc);vertical-align:middle}
.muted{color:var(--dim)}.tag{display:inline-block;padding:1px 8px;border-radius:10px;font-size:11px;border:1px solid var(--line)}
a{color:var(--acc)}
</style></head><body>`)
	b.WriteString("<h1>📊 Argus 质量控制台</h1><div class=\"sub\">")
	fmt.Fprintf(&b, "窗口 %v 天 · 生成于 %s · 数据来自 /stats /usage /metrics 既有聚合面（零新表）",
		stats["window_days"], time.Now().Format("2006-01-02 15:04:05"))
	b.WriteString("</div>")

	renderConsoleOverview(&b, stats)
	renderConsoleUsage(&b, usage)
	renderConsoleFailures(&b, metrics)
	renderConsoleFixLoop(&b, metrics)
	renderConsoleTrend(&b, s.evalTrend())
	renderConsoleRepos(&b, stats)

	b.WriteString("</body></html>")
	return b.String(), http.StatusOK
}

// evalTrend 的包级实现（转发壳见原文件）。
func evalTrend(s *Service) *qconsole.EvalTrend {
	path := "eval/results/baseline-latest.json"
	if s.Cfg != nil && s.Cfg.EvalReportPath != "" {
		path = s.Cfg.EvalReportPath
	}
	return qconsole.LoadEvalTrend(path)
}
