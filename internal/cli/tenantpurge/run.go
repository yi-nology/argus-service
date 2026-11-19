// tenantpurge 租户级级联清理 CLI（#109a）：`argus tenant-purge <tenant-id> [--dry-run]`。
//
// 退约/法务留存操作面：先 dry-run 预估各表命中，再实际执行。输出逐表计数 +
// 总数，执行形态（非 dry-run）额外打一行 BEGIN/END 方便日志审计。须显式给
// 租户 ID（无默认值——防手滑清 default 租户；default 也须显式写出）。
package tenantpurge

import (
	"context"
	"flag"
	"fmt"
	"os"
	"sort"

	"github.com/yi-nology/argus/internal/storage"
)

// Run 子命令入口（返回进程退出码）。
func Run(args []string) int {
	fs := flag.NewFlagSet("tenant-purge", flag.ContinueOnError)
	dry := fs.Bool("dry-run", false, "只预估各表命中计数，不落刀")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 || fs.Arg(0) == "" {
		fmt.Fprintln(os.Stderr, "用法: argus tenant-purge <tenant-id> [--dry-run]")
		return 2
	}
	tid := fs.Arg(0)
	dsn := os.Getenv("ARGUS_DB_DSN")
	store, err := storage.Open(dsn)
	if err != nil {
		fmt.Fprintln(os.Stderr, "tenant-purge: 打开存储失败:", err)
		return 1
	}
	defer store.Close()
	if !*dry {
		fmt.Printf("BEGIN tenant-purge tenant=%s\n", tid)
	}
	counts, err := store.PurgeTenant(context.Background(), tid, *dry)
	if err != nil {
		fmt.Fprintln(os.Stderr, "tenant-purge: 清理失败:", err)
		return 1
	}
	var total int64
	tables := make([]string, 0, len(counts))
	for t := range counts {
		tables = append(tables, t)
	}
	sort.Strings(tables)
	for _, t := range tables {
		fmt.Printf("%-28s %d\n", t, counts[t])
		total += counts[t]
	}
	mode := "PURGED"
	if *dry {
		mode = "DRY-RUN"
	}
	fmt.Printf("%s tenant=%s total=%d\n", mode, tid, total)
	return 0
}
