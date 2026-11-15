// r3tools_types.go —— R3/R4 工具入参与出参 wire 类型。

package requirement

type getIssueIn struct {
	Number string `json:"number" jsonschema:"description=issue 编号（纯数字）"`
}

type getIssueOut struct {
	Title string `json:"title,omitempty"`
	Body  string `json:"body,omitempty"`
	Error string `json:"error,omitempty"`
}

type getDetailIn struct {
	Ref    string `json:"ref" jsonschema:"description=截断提示中的详情编号"`
	Offset int64  `json:"offset,omitempty" jsonschema:"description=分页起点字节偏移（缺省 0 读第一页）"`
	Limit  int    `json:"limit,omitempty" jsonschema:"description=单页字节上限（缺省 8192，上限 16384）"`
}

type symIn struct {
	Name string `json:"name" jsonschema:"description=符号名（函数/方法/类型/变量名）"`
}

type traceIn struct {
	From string `json:"from" jsonschema:"description=起始符号名（裸名或 dir.Recv.Name 全名）"`
	To   string `json:"to" jsonschema:"description=目标符号名（裸名或 dir.Recv.Name 全名）"`
}

type symOut struct {
	Result string `json:"result"`
}
