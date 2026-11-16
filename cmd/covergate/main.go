// Package main 覆盖率棘轮门禁。
//
// 口径（与 spec §3.4 一致）：
//   - 全局覆盖率不得低于 60% 硬地板（无论 baseline 多低）；
//   - 全局覆盖率不得低于 baseline − 0.5%；
//   - 任一已有包不得低于 baseline − 0.5%；
//   - baseline 里不存在的包视为新包，必须 ≥70%；
//   - baseline 里的包在当前 profile 消失 → 红（防止静默删测）。
//
// `-write` 只允许把基线抬高/持平：逐包与全局取 max(旧基线, 当前)；旧基线里
// 的包在当前 profile 消失时保留旧地板并告警（不得借机下调）；新包仍要求 ≥70%。
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path"
	"sort"

	"golang.org/x/tools/cover"
)

// Baseline 是提交进仓库的覆盖率地板。
type Baseline struct {
	Global   float64            `json:"global"`
	Packages map[string]float64 `json:"packages"`
}

const (
	tolerance       = 0.005
	newPackageFloor = 0.70
	// globalFloor 是 kernel 的全局覆盖率硬地板：低于它一律红，与 baseline
	// 无关——防止基线自身曾经很低时，覆盖率继续向下漂移仍被判绿。
	globalFloor = 0.60
)

// compare 返回所有回归项；空表示通过。
func compare(base, cur Baseline) []string {
	var bad []string
	if cur.Global < globalFloor {
		bad = append(bad, fmt.Sprintf("global %.3f < 硬地板 %.2f", cur.Global, globalFloor))
	}
	if cur.Global+tolerance < base.Global {
		bad = append(bad, fmt.Sprintf("global %.3f -> %.3f（基线 %.3f）", base.Global, cur.Global, base.Global))
	}
	for p, b := range base.Packages {
		c, ok := cur.Packages[p]
		if !ok {
			bad = append(bad, fmt.Sprintf("%s 不再被覆盖（基线 %.3f）", p, b))
			continue
		}
		if c+tolerance < b {
			bad = append(bad, fmt.Sprintf("%s %.3f -> %.3f（基线 %.3f）", p, b, c, b))
		}
	}
	for p, c := range cur.Packages {
		if _, ok := base.Packages[p]; !ok && c < newPackageFloor {
			bad = append(bad, fmt.Sprintf("新包 %s %.3f < %.2f", p, c, newPackageFloor))
		}
	}
	sort.Strings(bad)
	return bad
}

// mergeBaselines 计算写入用的新基线：逐包与全局取 max(old, cur)。
// 旧基线里的包在当前 profile 消失时保留旧地板并返回告警——「包不在了」不是
// 下调地板的理由，否则删掉一个包的测试会静默换掉覆盖率绿。
func mergeBaselines(old, cur Baseline) (Baseline, []string) {
	out := Baseline{Packages: map[string]float64{}}
	if cur.Global > old.Global {
		out.Global = cur.Global
	} else {
		out.Global = old.Global
	}
	for p, b := range old.Packages {
		out.Packages[p] = b
	}
	for p, c := range cur.Packages {
		if b, ok := out.Packages[p]; !ok || c > b {
			out.Packages[p] = c
		}
	}
	var warn []string
	for p, b := range old.Packages {
		if _, ok := cur.Packages[p]; !ok {
			warn = append(warn, fmt.Sprintf("%s 在当前 profile 消失，保留旧地板 %.6f", p, b))
		}
	}
	sort.Strings(warn)
	return out, warn
}

// newPackageViolations 返回「旧基线不存在且当前覆盖率 <70%」的新包。
// 只用于 -write 的拒绝面：既有包的下调由 mergeBaselines 保住地板，不拒绝写入。
func newPackageViolations(base, cur Baseline) []string {
	var bad []string
	for p, c := range cur.Packages {
		if _, ok := base.Packages[p]; !ok && c < newPackageFloor {
			bad = append(bad, fmt.Sprintf("新包 %s %.3f < %.2f", p, c, newPackageFloor))
		}
	}
	sort.Strings(bad)
	return bad
}

// measure 从 coverage profile 计算全局与逐包语句覆盖率。
func measure(profiles []*cover.Profile) Baseline {
	out := Baseline{Packages: map[string]float64{}}
	type agg struct{ total, cov int64 }
	byPkg := map[string]*agg{}
	for _, p := range profiles {
		pkg := path.Dir(p.FileName) // coverage 的 FileName 是 import 路径
		a := byPkg[pkg]
		if a == nil {
			a = &agg{}
			byPkg[pkg] = a
		}
		for _, b := range p.Blocks {
			a.total += int64(b.NumStmt)
			if b.Count > 0 {
				a.cov += int64(b.NumStmt)
			}
		}
	}
	var total, covered int64
	for pkg, a := range byPkg {
		total += a.total
		covered += a.cov
		if a.total > 0 {
			out.Packages[pkg] = float64(a.cov) / float64(a.total)
		}
	}
	if total > 0 {
		out.Global = float64(covered) / float64(total)
	}
	return out
}

func loadBaseline(path string) (Baseline, error) {
	var b Baseline
	raw, err := os.ReadFile(path)
	if err != nil {
		return b, err
	}
	if err := json.Unmarshal(raw, &b); err != nil {
		return b, err
	}
	if b.Packages == nil {
		b.Packages = map[string]float64{}
	}
	return b, nil
}

func main() {
	prof := flag.String("profile", "coverage.out", "go test -coverprofile 产物")
	basePath := flag.String("baseline", "coverage-baseline.json", "基线文件")
	write := flag.Bool("write", false, "写基线（只允许抬高/持平）")
	flag.Parse()

	profs, err := cover.ParseProfiles(*prof)
	if err != nil {
		fmt.Fprintln(os.Stderr, "解析 coverage profile 失败:", err)
		os.Exit(2)
	}
	cur := measure(profs)

	if *write {
		old, err := loadBaseline(*basePath)
		if err != nil {
			if !os.IsNotExist(err) {
				// 基线存在但读不出：不能假装它是空的（那会把全部地板清掉）。
				fmt.Fprintln(os.Stderr, "读旧基线失败，拒绝重写:", err)
				os.Exit(2)
			}
			old = Baseline{Packages: map[string]float64{}}
		}
		if bad := newPackageViolations(old, cur); len(bad) > 0 {
			fmt.Fprintln(os.Stderr, "新包覆盖率低于 70%，拒绝写入：")
			for _, s := range bad {
				fmt.Fprintln(os.Stderr, s)
			}
			os.Exit(1)
		}
		merged, warns := mergeBaselines(old, cur)
		for _, w := range warns {
			fmt.Fprintln(os.Stderr, "警告:", w)
		}
		raw, _ := json.MarshalIndent(merged, "", "  ")
		raw = append(raw, '\n')
		if err := os.WriteFile(*basePath, raw, 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "写基线失败:", err)
			os.Exit(2)
		}
		return
	}

	base, err := loadBaseline(*basePath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "读基线失败（首次请跑 make cover-baseline）:", err)
		os.Exit(2)
	}
	if bad := compare(base, cur); len(bad) > 0 {
		fmt.Fprintln(os.Stderr, "覆盖率回归：")
		for _, s := range bad {
			fmt.Fprintln(os.Stderr, s)
		}
		os.Exit(1)
	}
	fmt.Printf("coverage ok: global=%.3f packages=%d\n", cur.Global, len(cur.Packages))
}
