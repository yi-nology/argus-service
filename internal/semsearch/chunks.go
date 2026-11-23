// chunks.go —— 工作副本扫描与 chunk 抽取：Go 走 go/parser 按顶层声明切
// （路径+行号+源码片段，doc 注释天然在内），其余文本文件按文件级首段切。
// 排除集与 codesearch 对齐；二进制（512B 窗口含 NUL）跳过。Go 解析顺带抽
// 符号调用边（v5.10.0：import 限定消歧 + 同接收者兄弟方法 + 裸名同目录优先，
// 节点键=NodeKey 契约，图面见 graph.go）——chunk 与边共用同一次 AST 解析，
// 图增强检索零额外解析成本。go.mod 的 module 声明经 modulePathOf 读取，是
// import 路径剥前缀定位仓库目录的输入。

package semsearch

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/oagentkit/textx/codeintel"
)

// 扫描与抽取上限（成本闸；MaxTotalChunks 是 embedding 调用预算的硬顶——
// 4000 段 × 一次 HTTP 批量调用，冷建索引在秒级、费用一次性的量级）。
const (
	maxDefaultFiles       = 2000 // 与 codegraph maxFiles 缺省同量级
	maxDefaultTotalChunks = 4000
	maxFileChunks         = 64  // 单文件 decl chunk 上限（巨型生成文件止损）
	fileChunkHeadLines    = 80  // 非 Go 文件级 chunk 摘要行数
	sniffLen              = 512 // 二进制嗅探窗口（与 codesearch 同口径）
)

// excludedDirs 目录排除集（与 codesearch 一致；此处是扫描剪枝，非安全边界）。
var excludedDirs = map[string]bool{
	".git": true, "vendor": true, "node_modules": true, "testdata": true,
	"dist": true, "third_party": true, "__pycache__": true,
	".venv": true, "venv": true, "target": true,
}

// codeExts 参与索引的扩展名（codeintel.ChunkExts 单源，map 形态供热查）：
// 代码为主 + 仓库文档（.md 常载规范与架构语义，审查场景价值高）。
// go.mod 不在集内——它供 modulePathOf 读 module 声明，不作为 chunk 进索引。
var codeExts = func() map[string]bool {
	m := make(map[string]bool, len(codeintel.ChunkExts))
	for _, e := range codeintel.ChunkExts {
		m[e] = true
	}
	return m
}()

// scannedFile 扫描产物：相对路径 + 指纹 + 内容（抽取与指纹判定共用一次读取）。
type scannedFile struct {
	rel     string
	fp      fileFP
	content []byte
}

// scanDir 收集目录下参与索引的文件（排除集剪枝 + 扩展名过滤 + 二进制跳过），
// 按相对路径排序保证遍历与截断的确定性。
func scanDir(dir string) ([]scannedFile, error) {
	var out []scannedFile
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // 单点不可读不阻断扫描（codesearch 同宽容度）
		}
		if d.IsDir() {
			if excludedDirs[d.Name()] && path != dir {
				// 只跳该目录（v6.1.1 热修：此前 SkipAll 中止整棵树——真实仓库
				// 必有 vendor/testdata/.git 之类，索引恒空 → search_sem 在所有
				// 真实仓库上静默失效；codesearch 用 SkipDir 故 search_code 一直
				// 正常。单测 fixture 是裸 TempDir 未复现，产线实弹抓现形）。
				return filepath.SkipDir
			}
			return nil
		}
		// 安全（v3.133.3）：只读常规文件——软链文件会被 os.ReadFile 跟随，
		// 恶意 PR 提交 link -> /proc/self/environ 即把宿主全部密钥读进索引/
		// 工具输出（实测泄漏确认）。与 codesearch 的 IsRegular 守卫同口径。
		if !d.Type().IsRegular() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(d.Name()))
		if !codeExts[ext] {
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil {
			return nil
		}
		content, rerr := os.ReadFile(path)
		if rerr != nil || isBinary(content) {
			return nil
		}
		rel, rerr := filepath.Rel(dir, path)
		if rerr != nil {
			return nil
		}
		out = append(out, scannedFile{
			rel:     rel,
			fp:      fileFP{size: info.Size(), mtime: info.ModTime().UnixNano()},
			content: content,
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	// 插入排序键：扫描序本身已按字典序（WalkDir 词序），显式排序兜底平台差异。
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].rel < out[j-1].rel; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out, nil
}
