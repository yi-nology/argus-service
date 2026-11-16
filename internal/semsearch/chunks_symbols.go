package semsearch

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
)

// symEdge 原始调用边（Refresh 中间产物，v5.10.0）：caller 是调用方节点键
// （NodeKey 契约：dir.Base / dir.Recv.Base），callerDir 是调用方所在仓库相对
// 目录（裸名边同目录优先解析的输入）。callee 按 qualified 二态——
// qualified=true：已解析的 dir 限定目标键（import 剥模块前缀的跨包目标、
// 同接收者兄弟方法 callerDir.recvBase.Sel、modulePath 缺失时的 alias 猜测键），
// rebuild 存在性过滤后建边，目标不存在降级 bare 末段回退；
// qualified=false：裸名（包内裸调用、未知变量限定 X.Sel 降末段、stdlib/外部
// import 末段），rebuild 同目录候选优先（Go 包级裸调用不跨包），无候选走全局
// bare 回退（dot-import 跨包裸调用经此保召回）。解析与剪枝在 Refresh 收尾
// 统一做（graph.go）。
type symEdge struct {
	caller    string
	callerDir string
	callee    string
	qualified bool
}

// extractChunks 单文件 → (chunk 列表, 调用边)。modulePath 是 go.mod 的 module
// 声明（import 限定消歧输入；缺失传空串——qualified 目标降 alias 猜测/裸名
// 回退保召回）。Go 按顶层声明（含 doc 注释）切并顺带抽边，其余按文件级首段
// （无边）。Go 解析失败降级文件级 chunk（语法坏文件也有路径与文本可检索，
// 且无边）。
func extractChunks(rel string, content []byte, modulePath string) ([]Chunk, []symEdge) {
	if strings.HasSuffix(rel, ".go") {
		if chunks, edges := goDeclChunks(rel, content, modulePath); len(chunks) > 0 {
			return chunks, edges
		}
	}
	return []Chunk{fileChunk(rel, content)}, nil
}

// goDeclChunks Go 文件按顶层声明切 chunk 并顺带抽调用边（v5.10.0，同一次解析
// 双产出）：文本 = 「rel:起始行\n」+ 从 doc 注释（如有）到声明结束的源码片段
// ——路径、签名、注释一并进 embedding 输入，这是「结构化摘要」路线的落地。
// 边抽取规则（词法级边界不变：接口分派/动态派发不可见）——
//
//	裸名 F()：qualified=false 裸名边（同目录优先解析，Go 包级裸调用不跨包）
//	pkg.F()：import 表命中——path 剥模块前缀得仓库相对 dir → qualified=true
//	         目标 dir.F；剥不动（stdlib/外部模块）→ qualified=false 末段回退；
//	         modulePath 空 → alias 当目标目录末段猜测（键 alias.Sel，未命中
//	         由 rebuild 降 bare 回退保召回）
//	x.Sel() 且 x==接收者参数名：同接收者兄弟方法 → qualified=true 目标
//	         callerDir.recvBase.Sel（T 可能在同包其他文件定义，同目录键正确）
//	x.Sel() 其余（var 限定）：无类型信息不可分 → 降 qualified=false 裸名末段
//	         保召回（v5.9.0 末段归一的召回面保留）
//
// 闭包体归外层函数（随其执行）；顶层 var 初始化器里的 FuncLit 归包级 init、
// 不归属函数符号，不抽。
func goDeclChunks(rel string, content []byte, modulePath string) ([]Chunk, []symEdge) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, rel, content, parser.ParseComments)
	if err != nil || f == nil {
		return nil, nil
	}
	dir := dirOf(rel)
	src := content
	var chunks []Chunk
	var edges []symEdge
	for _, decl := range f.Decls {
		startPos := decl.Pos()
		symbol := declSymbol(decl, dir)
		if gd, ok := decl.(*ast.GenDecl); ok && gd.Doc != nil {
			startPos = gd.Doc.Pos()
		}
		if fd, ok := decl.(*ast.FuncDecl); ok && fd.Doc != nil {
			startPos = fd.Doc.Pos()
		}
		start := fset.Position(startPos)
		end := fset.Position(decl.End())
		if start.Line <= 0 || end.Offset > len(src) || start.Offset > end.Offset {
			continue
		}
		text := string(src[start.Offset:end.Offset])
		chunks = append(chunks, Chunk{
			Path:   rel,
			Line:   start.Line,
			Symbol: symbol,
			Text:   rel + ":" + itoa(start.Line) + "\n" + clipText(text),
		})
	}
	// 调用边抽取（与 chunk 同一次解析的副产品）：FuncDecl 体 CallExpr。
	imports := importTable(f)
	for _, decl := range f.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Body == nil {
			continue
		}
		caller := declSymbol(fd, dir)
		recvParam, recvBase := receiverInfo(fd)
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if e := callEdge(caller, dir, call, imports, recvParam, recvBase, modulePath); e != nil {
				edges = append(edges, *e)
			}
			return true
		})
	}
	return chunks, edges
}

// callEdge 单个调用位 → symEdge（nil=不抽：链式 a.b.c() / 方法字面量/类型转换
// 形态词法不可分）。receiverInfo 的接收者参数名与基类型名供兄弟方法判定。
func callEdge(caller, dir string, call *ast.CallExpr, imports map[string]string, recvParam, recvBase, modulePath string) *symEdge {
	switch fun := call.Fun.(type) {
	case *ast.Ident: // 包内裸调用（含 dot-import 的跨包裸名——rebuild 全局回退保召回）
		return &symEdge{caller: caller, callerDir: dir, callee: fun.Name}
	case *ast.SelectorExpr:
		id, ok := fun.X.(*ast.Ident)
		if !ok {
			return nil // 链式/字面量：不抽
		}
		if path, imported := imports[id.Name]; imported {
			return importEdge(caller, dir, id.Name, path, fun.Sel.Name, modulePath)
		}
		if recvParam != "" && recvBase != "" && id.Name == recvParam {
			// 同接收者兄弟方法：qualified 目标 callerDir.recvBase.Sel。
			return &symEdge{caller: caller, callerDir: dir,
				callee: NodeKey(dir, recvBase, fun.Sel.Name), qualified: true}
		}
		// 未知变量限定（var.M）：类型不可见——降裸名末段保召回。
		return &symEdge{caller: caller, callerDir: dir, callee: fun.Sel.Name}
	}
	return nil
}

// importEdge import 限定调用 → symEdge：modulePath 已知时剥前缀定位仓库相对
// 目录即 qualified 目标，剥不动（stdlib/外部模块，仓库内无此目录）降
// qualified=false 末段回退；modulePath 空（go.mod 缺失/不可读）→ alias 当
// 目标目录末段猜测（键 alias.Sel，rebuild 未命中降 bare 回退保召回）。
func importEdge(caller, dir, alias, path, sel, modulePath string) *symEdge {
	if modulePath != "" {
		switch {
		case path == modulePath: // import 的是模块根包：dir 即根
			return &symEdge{caller: caller, callerDir: dir,
				callee: NodeKey("", "", sel), qualified: true}
		case strings.HasPrefix(path, modulePath+"/"):
			return &symEdge{caller: caller, callerDir: dir,
				callee: NodeKey(path[len(modulePath)+1:], "", sel), qualified: true}
		}
		return &symEdge{caller: caller, callerDir: dir, callee: sel}
	}
	return &symEdge{caller: caller, callerDir: dir,
		callee: NodeKey(alias, "", sel), qualified: true}
}

// importTable 文件 import 表：alias → import path（无命名 alias 取 import path
// 的末个路径段——按 "/" 切，非 lastSeg 的点切）。
// 点导入与空白导入不入表——点导入的跨包裸调用是裸名形态，走 rebuild 全局
// bare 回退保召回；空白导入不可引用。
func importTable(f *ast.File) map[string]string {
	if len(f.Imports) == 0 {
		return nil
	}
	m := make(map[string]string, len(f.Imports))
	for _, imp := range f.Imports {
		path := strings.Trim(imp.Path.Value, `"`)
		if path == "" {
			continue
		}
		alias := ""
		if imp.Name != nil {
			alias = imp.Name.Name
		}
		if alias == "." || alias == "_" {
			continue
		}
		if alias == "" {
			if i := strings.LastIndex(path, "/"); i >= 0 {
				alias = path[i+1:]
			} else {
				alias = path
			}
		}
		m[alias] = path
	}
	return m
}

// modulePathOf 读 dir/go.mod 的 module 声明（stdlib 词法解析，不引 x/mod——
// L0 纪律；缺失/解析失败返回空串，import 限定降猜测/裸名路径保召回）。
func modulePathOf(dir string) string {
	b, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "module") {
			continue
		}
		if len(line) > len("module") {
			if c := line[len("module")]; c != ' ' && c != '\t' {
				continue // moduleX 等前缀误命中
			}
		}
		rest := strings.TrimSpace(line[len("module"):])
		if i := strings.IndexAny(rest, " \t"); i >= 0 {
			rest = rest[:i] // 剥行尾注释
		}
		if rest = strings.Trim(rest, `"`); rest != "" {
			return rest
		}
	}
	return ""
}

// declSymbol 顶层声明节点键（NodeKey 契约：FuncDecl 名带接收者基类型；GenDecl
// 首个 spec 名；匿名结构取类型名）。
func declSymbol(decl ast.Decl, dir string) string {
	switch d := decl.(type) {
	case *ast.FuncDecl:
		recv := ""
		if d.Recv != nil {
			recv = recvTypeName(d)
		}
		return NodeKey(dir, recv, d.Name.Name)
	case *ast.GenDecl:
		for _, spec := range d.Specs {
			switch s := spec.(type) {
			case *ast.TypeSpec:
				return NodeKey(dir, "", s.Name.Name)
			case *ast.ValueSpec:
				if len(s.Names) > 0 {
					return NodeKey(dir, "", s.Names[0].Name)
				}
			}
		}
	}
	return ""
}

// receiverInfo 方法接收者信息：参数名（兄弟方法判定用）与基类型名（兄弟目标
// 键用）。无接收者/无名参数/泛型接收者（*T[T] 形态）返回空——兄弟规则保守跳过。
func receiverInfo(fd *ast.FuncDecl) (name, base string) {
	if fd.Recv == nil || len(fd.Recv.List) == 0 || len(fd.Recv.List[0].Names) == 0 {
		return "", ""
	}
	name = fd.Recv.List[0].Names[0].Name
	base = recvTypeName(fd)
	return name, base
}

// recvTypeName 方法接收者的基类型名（*T / T → T）。
func recvTypeName(fd *ast.FuncDecl) string {
	expr := fd.Recv.List[0].Type
	if star, ok := expr.(*ast.StarExpr); ok {
		expr = star.X
	}
	if id, ok := expr.(*ast.Ident); ok {
		return id.Name
	}
	return ""
}
