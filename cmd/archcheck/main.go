// Package main 是 argus 的架构门禁运行器：加载 architecture-policy.yaml，
// 调 goagent-kernel/archcheck 执行声明面，并在 kernel 判据之外补两层棘轮：
//
//  1. 例外数上限：--max-exceptions 从 arch-exceptions.baseline 读取，只许降；
//     基线缺失/非法即退出码 2（fail closed，绝不跳过检查）；--write-baseline
//     拒绝把上限写高。
//  2. 阈值发现基线：thresholds 的 ThresholdFindings 不被 kernel exceptions
//     覆盖（HasBlocking 单独判），用 arch-thresholds.baseline 做收缩棘轮。
//
// 阈值基线按 key→数值 存储（文件行数 / 实质方法数）：同一路径行数增长即红，
// 拆分后旧 key 消失、新 key 更小，不误报。阈值发现是聚合字符串，必须解析，
// 不能整行比对——否则「47 个」变「46 个」会被当成新发现。
//
// policy 解析失败/门禁给不出结论时退出码 2，绝不显示成通过。
package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/yi-nology/goagent-kernel/archcheck"
)

// parseThreshold 把 kernel 的聚合阈值字符串拆成 key→数值。
// 例：
//
//	internal/impactview/html.go (1554 行 > 400)          -> html.go, 1554
//	internal/runner.Runner (62 个方法 = 2 转发壳 + ...)  -> Runner, 62
//
// 标题行（以「个:」结尾）跳过；同一 key 取最大值（防重复行）。
func parseThreshold(raw []string) map[string]int {
	out := map[string]int{}
	for _, block := range raw {
		for _, line := range strings.Split(block, "\n") {
			t := strings.TrimSpace(line)
			if t == "" || strings.HasSuffix(t, "个:") {
				continue
			}
			i := strings.Index(t, " (")
			if i <= 0 {
				continue
			}
			key := strings.TrimSpace(t[:i])
			rest := t[i+2:]
			var n int
			if _, err := fmt.Sscanf(rest, "%d", &n); err != nil || n <= 0 {
				continue
			}
			if old, ok := out[key]; !ok || n > old {
				out[key] = n
			}
		}
	}
	return out
}

func readThresholdBase(path string) map[string]int {
	out := map[string]int{}
	f, err := os.Open(path)
	if err != nil {
		return out
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) != 2 {
			continue
		}
		n, err := strconv.Atoi(strings.TrimSpace(parts[1]))
		if err != nil {
			continue
		}
		out[strings.TrimSpace(parts[0])] = n
	}
	return out
}

func writeThresholdBase(path string, cur map[string]int) error {
	keys := make([]string, 0, len(cur))
	for k := range cur {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&b, "%s\t%d\n", k, cur[k])
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

// regressions 返回基线外的新发现与数值增长；缩小的 key 不算回归。
func regressions(cur, base map[string]int) []string {
	var out []string
	for k, v := range cur {
		b, ok := base[k]
		if !ok {
			out = append(out, fmt.Sprintf("%s\t%d\t新增", k, v))
			continue
		}
		if v > b {
			out = append(out, fmt.Sprintf("%s\t%d\t增长（基线 %d）", k, v, b))
		}
	}
	sort.Strings(out)
	return out
}

func readInt(path string) int {
	b, err := os.ReadFile(path)
	if err != nil {
		return -1
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		return -1
	}
	return n
}

// exceptionCapViolation 判断 -write-baseline 是否试图抬高例外上限。
// 旧基线缺失（-1）是首次写入口，允许；旧基线存在时只允许持平或收缩。
func exceptionCapViolation(oldCap, cur int) error {
	if oldCap >= 0 && cur > oldCap {
		return fmt.Errorf("例外数 %d 超过既有基线 %d：只允许收缩", cur, oldCap)
	}
	return nil
}

// resolveMaxExceptions 解析检查模式的例外上限：显式 -max-exceptions 优先；
// 否则读基线。基线缺失或非法时返回错误（fail closed）——绝不能退化成 -1
// 而静默跳过例外棘轮。
func resolveMaxExceptions(flagVal int, path string) (int, error) {
	if flagVal >= 0 {
		return flagVal, nil
	}
	n := readInt(path)
	if n < 0 {
		return 0, fmt.Errorf("例外基线 %s 缺失或非法（拒绝跳过例外检查）", path)
	}
	return n, nil
}

func isExcused(v archcheck.Violation, exs []archcheck.Exception) bool {
	today := time.Now().UTC().Format("2006-01-02")
	for _, ex := range exs {
		if ex.Rule != v.Rule || ex.Path != v.Path || ex.Line != v.Line {
			continue
		}
		if ex.Until == "" || ex.Until >= today {
			return true
		}
	}
	return false
}

func main() {
	policy := flag.String("policy", "architecture-policy.yaml", "policy 文件")
	maxEx := flag.Int("max-exceptions", -1, "例外数上限；-1 不检查")
	thr := flag.String("thresholds-baseline", "arch-thresholds.baseline", "阈值发现基线")
	writeBase := flag.Bool("write-baseline", false, "写阈值/例外基线（只允许收缩）")
	flag.Parse()

	pol, err := archcheck.Load(*policy)
	if err != nil {
		fmt.Fprintln(os.Stderr, "policy 加载失败:", err)
		os.Exit(2)
	}
	res, err := archcheck.Run(".", pol)
	if err != nil {
		fmt.Fprintln(os.Stderr, "门禁给不出结论:", err)
		os.Exit(2)
	}

	for _, v := range res.Violations {
		if isExcused(v, pol.Exceptions) {
			fmt.Printf("EXCUSED %s:%d [%s] %s\n", v.Path, v.Line, v.Rule, v.Text)
			continue
		}
		fmt.Printf("%s:%d [%s] %s\n", v.Path, v.Line, v.Rule, v.Text)
	}
	for _, f := range res.ThresholdFindings {
		fmt.Println("THRESHOLD", f)
	}
	cur := parseThreshold(res.ThresholdFindings)
	fmt.Printf("blocking=%d warnings=%d excused=%d exceptions=%d thresholds=%d\n",
		res.Blocking, res.Warnings, res.Excused, len(res.Exceptions), len(cur))

	if *writeBase {
		base := readThresholdBase(*thr)
		if len(base) > 0 {
			if bad := regressions(cur, base); len(bad) > 0 {
				fmt.Fprintln(os.Stderr, "阈值基线只允许收缩，拒绝写入：")
				for _, s := range bad {
					fmt.Fprintln(os.Stderr, s)
				}
				os.Exit(1)
			}
		}
		if err := writeThresholdBase(*thr, cur); err != nil {
			fmt.Fprintln(os.Stderr, "写阈值基线失败:", err)
			os.Exit(2)
		}
		oldCap := readInt("arch-exceptions.baseline")
		if err := exceptionCapViolation(oldCap, len(res.Exceptions)); err != nil {
			fmt.Fprintln(os.Stderr, "例外基线只允许收缩，拒绝写入:", err)
			os.Exit(1)
		}
		if err := os.WriteFile("arch-exceptions.baseline", []byte(fmt.Sprintf("%d\n", len(res.Exceptions))), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "写例外基线失败:", err)
			os.Exit(2)
		}
		return
	}

	fail := false
	exceptionCap, err := resolveMaxExceptions(*maxEx, "arch-exceptions.baseline")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if len(res.Exceptions) > exceptionCap {
		fmt.Fprintf(os.Stderr, "例外数 %d 超过基线 %d\n", len(res.Exceptions), exceptionCap)
		fail = true
	}
	if bad := regressions(cur, readThresholdBase(*thr)); len(bad) > 0 {
		fmt.Fprintln(os.Stderr, "出现基线外的新阈值发现：")
		for _, s := range bad {
			fmt.Fprintln(os.Stderr, s)
		}
		fail = true
	}
	if res.HasBlocking() || fail {
		os.Exit(1)
	}
}
