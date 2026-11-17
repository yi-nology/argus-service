// printalias.go —— 指纹别名 + 近重复匹配层：阈值、相似度度量与同 PR 近重复检索。

package storage

import (
	"strings"
	"time"

	"gorm.io/gorm"

	akreportutil "github.com/oagentkit/textx/reportutil"
	"github.com/oagentkit/textx/textutil"
	"github.com/yi-nology/argus/internal/domain"
)

// PrintAlias 别名映射：alias_fp（某轮出现的新措辞指纹）→ print_fp（既有条目的
// 规范指纹）。一行多别名是常态（同一问题跨多轮的不同措辞）。
// v3.9.2 起 PR 四列并入主键：指纹按 PR 归属（idx_print 唯一索引含 number），
// 旧形态 alias_fp 全局唯一主键下，两个 PR 出现同款措辞漂移（同一新指纹）时
// 第二个 PR 建别名撞主键、UpsertPrint 整体回滚。存量由 print_alias_pr_scope
// 迁移按 print_fp 反查 PR 回填（命中多 PR 逐 PR 复制，孤儿丢弃）。
type PrintAlias struct {
	TenantID  string    `gorm:"column:tenant_id;size:64;not null;default:default;primaryKey"`
	Platform  string    `gorm:"column:platform;primaryKey;size:16;not null"`
	Owner     string    `gorm:"column:owner;primaryKey;size:128;not null"`
	Repo      string    `gorm:"column:repo;primaryKey;size:128;not null"`
	Number    string    `gorm:"column:number;primaryKey;size:16;not null"`
	AliasFP   string    `gorm:"column:alias_fp;primaryKey;size:64;not null"`
	PrintFP   string    `gorm:"column:print_fp;size:64;index"`
	UpdatedAt time.Time `gorm:"column:updated_at"`
}

func (PrintAlias) TableName() string { return "print_aliases" }

// nearSimilarityThreshold 近重复判定阈值：归一化评语的 rune 二元组重叠率
// （按较短侧）≥ 0.6 视为同一问题的不同措辞。0.6 下 SQL 注入 vs err 未检查
// （同位置不同问题）远达不到，同一问题的措辞微调稳过。
const nearSimilarityThreshold = 0.6

// vagueNearSuppressedThreshold 模糊意见（line=0 的多文件聚合/摘要形态）专属
// 吸收阈值（v3.133.0）：实测同一问题的彻底重述与"同函数不同问题"的重叠率区间
// 互锁（0.237~0.319 vs 0.364），全局阈值无解——但模糊意见本身无行号锚点、
// 主张精度低，对人工已判误报的行按 ≥0.2 吸收是合理不对称：精确意见（line>0）
// 仍走 0.6/0.4 两级不动。0.2 下界与真实跨问题对（0.02~0.06）有充分间距。
const vagueNearSuppressedThreshold = 0.2

// looseNearSuppressedThreshold 抑制行专属宽松阈值（v3.130.0）：人工已判误报
// （suppressed）的行按 ≥0.4 吸收**重措辞**复现——2026-10-09 实测：同一处硬编码
// 密钥问题被模型改写为「新增了硬编码的生产密钥…」后与原措辞仅 ~0.43 相似，
// 0.6 阈值失守、抑制被绕过复活。只对 suppressed 行放宽：普通行仍按 0.6 做
// 合并/别名，宽松合并普通行会吞掉真正的新问题（同文件近邻的不同问题）。
const looseNearSuppressedThreshold = 0.4

// commentNear 归一化后互含或二元组重叠率 ≥0.6（v3.7.0 一般近重复语义）。
func commentNear(a, b string) bool { return commentMatch(a, b, nearSimilarityThreshold) }

// commentNearLoose 抑制行宽松近重复（≥0.4）：同 commentNear 度量、阈值放宽。
func commentNearLoose(a, b string) bool { return commentMatch(a, b, looseNearSuppressedThreshold) }

// commentNearVague 模糊意见吸收（≥0.2，仅 suppressed 行 + line=0 + 文件组件
// 相交三重约束下使用）。
func commentNearVague(a, b string) bool { return commentMatch(a, b, vagueNearSuppressedThreshold) }

// fileSetIntersect 逗号分隔多文件形态的组件交集（"db.go,util.go" vs "db.go"
// = 命中；同值恒命中）。
func fileSetIntersect(a, b string) bool {
	as := strings.Split(a, ",")
	for _, x := range strings.Split(b, ",") {
		x = strings.TrimSpace(x)
		if x == "" {
			continue
		}
		for _, y := range as {
			if strings.TrimSpace(y) == x {
				return true
			}
		}
	}
	return false
}

// commentMatch 归一化后互含（无阈值）或二元组重叠率达标。
// 集合构造复用 agentkit v0.10.0 沉淀的 textutil.BigramSet；度量刻意不用
// textutil.Jaccard（并集分母，更严）——阈值按短侧重叠率校准，换 Jaccard 会
// 漏掉合法的措辞漂移匹配（测试钉死该行为）。
func commentMatch(a, b string, threshold float64) bool {
	na, nb := akreportutil.NormalizeComment(a), akreportutil.NormalizeComment(b)
	if na == "" || nb == "" {
		return false
	}
	if strings.Contains(na, nb) || strings.Contains(nb, na) {
		return true
	}
	sa, sb := textutil.BigramSet(na), textutil.BigramSet(nb)
	if len(sa) == 0 || len(sb) == 0 {
		return false
	}
	small, large := sa, sb
	if len(sb) < len(sa) {
		small, large = sb, sa
	}
	hit := 0
	for g := range small {
		if large[g] {
			hit++
		}
	}
	return float64(hit) >= float64(len(small))*threshold
}

// resolveAliasFP 别名一跳解析（alias → 规范指纹；无映射原样返回）。
// PR 域限定（v3.9.2）：别名按 PR 归属，同 alias_fp 在不同 PR 可指向各自规范行。
func resolveAliasFP(tx *gorm.DB, platform, owner, repo, number, fingerprint string) string {
	var a PrintAlias
	if err := tx.Where("platform = ? AND owner = ? AND repo = ? AND number = ? AND alias_fp = ?",
		platform, owner, repo, number, fingerprint).First(&a).Error; err != nil {
		return fingerprint
	}
	return a.PrintFP
}

// findNearPrint 在同 PR 同文件的既有条目里找近重复（行号相容 + 评语近重复）。
// suppressed 行同样是候选——措辞漂移的复现应当延续抑制，而不是绕开抑制复活。
// 三级匹配（v3.8.0 两级 + v3.130.0 抑制行宽松级）：
//  1. exact：规范化评语**完全相等**——行号不限距。同 PR 推新提交使行号整体位移
//     >±2 时，±2 窗口救不回（抑制/复现/regret 全部失忆、被抑制意见复活重发）；
//     同文件同句评语视为行号漂移而非新问题。这也让 SuppressPrintNear 预插的
//     line=0 行可以被真实意见回收。优先取行号相容者（真两处同模板意见时轮内
//     byNC ±2 闸门已各自成行，此处行号相容优先可减少跨位置误并）。
//  2. near：评语近重复（二元组重叠 ≥0.6）且行号 ±2——v3.7.0 原语义不动。
//  3. loose：suppressed 行的宽松近重复（≥0.4）且行号 ±2（v3.130.0）——重措辞
//     复现不再绕过人工抑制；只放宽已判误报的行，普通行不动。
//
// 返回 nil 表示无近重复；exact=true 表示走的是完全同评语通道（调用方可据此
// 让行号跟随新值，行内评论位置不漂移）。
func findNearPrint(tx *gorm.DB, platform, owner, repo, number string, f domain.Finding) (*FindingPrint, bool, error) {
	// 不在 SQL 层限行距：exact 通道必须看到行号超窗的同评语行（行号漂移场景）；
	// 同 PR 同文件条目规模小（试点 <50），Limit(200) 足够，分级在内存做。
	// 查询面含多文件组件（"db.go,util.go" 要看见 "db.go" 的行）；内存层按
	// 匹配级别各自收口。
	candFiles := map[string]bool{f.File: true}
	for _, part := range strings.Split(f.File, ",") {
		if p := strings.TrimSpace(part); p != "" {
			candFiles[p] = true
		}
	}
	fileList := make([]string, 0, len(candFiles))
	for f := range candFiles {
		fileList = append(fileList, f)
	}
	var rows []FindingPrint
	if err := tx.Where("platform = ? AND owner = ? AND repo = ? AND number = ? AND file IN ?",
		platform, owner, repo, number, fileList).
		Order("last_seen_at DESC").Limit(200).Find(&rows).Error; err != nil {
		return nil, false, err
	}
	nc := akreportutil.NormalizeComment(f.Comment)
	var exact, near, loose *FindingPrint
	for i := range rows {
		if nc != "" && akreportutil.NormalizeComment(rows[i].Comment) == nc {
			// 完全同评语：行号相容者优先，否则取最新（rows 已按 last_seen_at DESC）
			if exact == nil || (linesClose(rows[i].Line, f.Line) && !linesClose(exact.Line, f.Line)) {
				exact = &rows[i]
			}
			continue
		}
		switch {
		case f.Line > 0 && linesClose(rows[i].Line, f.Line):
			switch {
			case near == nil && commentNear(rows[i].Comment, f.Comment):
				near = &rows[i]
			case loose == nil && rows[i].Status == PrintSuppressed && commentNearLoose(rows[i].Comment, f.Comment):
				loose = &rows[i]
			}
		case f.Line == 0 && loose == nil && rows[i].Status == PrintSuppressed &&
			fileSetIntersect(rows[i].File, f.File) && commentNearVague(rows[i].Comment, f.Comment):
			// 模糊意见吸收（v3.133.0）：line=0 聚合形态无行号锚点，靠文件组件
			// 相交 + 0.2 话题重叠吸收进被抑制行——彻底重述不再绕过抑制。
			loose = &rows[i]
		}
	}
	if exact != nil {
		return exact, true, nil
	}
	if near != nil {
		return near, false, nil
	}
	if loose != nil {
		return loose, false, nil
	}
	return nil, false, nil
}

// absDiff 行距（v3.76.0 用内建 max 惯用法替代手写分支）。
func absDiff(a, b int) int { return max(a-b, b-a) }

func linesClose(a, b int) bool { return absDiff(a, b) <= 2 }
