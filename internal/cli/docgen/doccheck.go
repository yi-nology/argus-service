package docgen

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// errDocIssue 标记文档门禁违规（退出码 1），区别于 IO/工具错误（退出码 2）。
var errDocIssue = errors.New("文档门禁未通过")

var mdLinkRe = regexp.MustCompile(`\[[^\]]*\]\(([^)\s]+)(?:\s+"[^"]*")?\)`)

// checkDocIndex 要求 guide/ops/design 下每个 .md 都被 docs/README.md 引用。
// 索引里既接受仓库相对路径（docs/guide/a.md），也接受索引视角相对路径（guide/a.md）。
func checkDocIndex(root string) error {
	index, err := os.ReadFile(filepath.Join(root, "docs/README.md"))
	if err != nil {
		return err
	}
	var missing []string
	for _, dir := range []string{"docs/guide", "docs/ops", "docs/design"} {
		base := filepath.Join(root, dir)
		if _, statErr := os.Stat(base); os.IsNotExist(statErr) {
			continue
		}
		err := filepath.WalkDir(base, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(d.Name(), ".md") {
				return nil
			}
			rel, relErr := filepath.Rel(root, path)
			if relErr != nil {
				return relErr
			}
			rel = filepath.ToSlash(rel)
			short := strings.TrimPrefix(rel, "docs/")
			if !strings.Contains(string(index), rel) && !strings.Contains(string(index), short) {
				missing = append(missing, rel)
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("%w: docs/README.md 索引缺少 %s", errDocIssue, strings.Join(missing, ", "))
	}
	return nil
}

// checkDocLinks 校验根入口文档与 docs 非 archive 文档中的相对 .md 链接。
// archive/superpowers/research 为历史区，允许保留失效路径。
func checkDocLinks(root string) error {
	files := []string{
		"README.md", "ARCHITECTURE.md", "CONTRIBUTING.md", "SECURITY.md", "deploy/README.md",
	}
	docsDir := filepath.Join(root, "docs")
	if _, statErr := os.Stat(docsDir); statErr == nil {
		err := filepath.WalkDir(docsDir, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, relErr := filepath.Rel(root, path)
			if relErr != nil {
				return relErr
			}
			rel = filepath.ToSlash(rel)
			if d.IsDir() {
				if rel == "docs/archive" || rel == "docs/superpowers" {
					return filepath.SkipDir
				}
				return nil
			}
			if strings.HasSuffix(rel, ".md") {
				files = append(files, rel)
			}
			return nil
		})
		if err != nil {
			return err
		}
	} else if !os.IsNotExist(statErr) {
		return statErr
	}

	var issues []string
	for _, rel := range files {
		raw, err := os.ReadFile(filepath.Join(root, rel))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		src := stripFencedBlocks(string(raw))
		for _, m := range mdLinkRe.FindAllStringSubmatchIndex(src, -1) {
			target := src[m[2]:m[3]]
			if strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "https://") ||
				strings.HasPrefix(target, "mailto:") || strings.HasPrefix(target, "tel:") ||
				strings.HasPrefix(target, "data:") || strings.HasPrefix(target, "#") ||
				strings.HasPrefix(target, "/") {
				continue
			}
			target = strings.SplitN(target, "#", 2)[0]
			if !strings.HasSuffix(strings.ToLower(target), ".md") {
				continue
			}
			resolved := filepath.Clean(filepath.Join(root, filepath.Dir(rel), target))
			if _, statErr := os.Stat(resolved); statErr != nil {
				line := 1 + strings.Count(src[:m[0]], "\n")
				issues = append(issues, fmt.Sprintf("%s:%d → %s", rel, line, target))
			}
		}
	}
	if len(issues) > 0 {
		return fmt.Errorf("%w:\n%s", errDocIssue, strings.Join(issues, "\n"))
	}
	return nil
}

// stripFencedBlocks 把 ``` / ~~~ 围栏代码块替换为空行（保留行数，行号不漂移），
// 避免把示例里的 markdown 链接当成真实引用。
func stripFencedBlocks(src string) string {
	lines := strings.Split(src, "\n")
	inFence := false
	for i, line := range lines {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "```") || strings.HasPrefix(t, "~~~") {
			inFence = !inFence
			lines[i] = ""
			continue
		}
		if inFence {
			lines[i] = ""
		}
	}
	return strings.Join(lines, "\n")
}