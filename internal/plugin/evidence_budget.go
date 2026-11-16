package plugin

// 证据读取任务级预算（v7.5 #69）：与 evidence.go 的核验主流程同包拆分，
// 单文件保持在架构阈值（400 行）以内。

// 证据读取任务级预算（v7.5 #69）：单次批量核验（一个任务的 finish 步骤）
// 的引用数与总读取字节上界。每文件读取另由 SourceReader 实现按大小/行数
// 设限（runner workcopySourceReader 缺省 2MiB / 5 万行 / 单文件超时）。
const (
	maxEvidenceRefsPerTask  = 1024
	maxEvidenceBytesPerTask = 16 << 20 // 16 MiB
)

// evidenceBudget 任务级预算记账（nil 安全：nil = 不设限，Claim 级路径）。
type evidenceBudget struct {
	maxRefs  int
	maxBytes int
	refs     int
	bytes    int
	files    map[string]int // path → 文件字节数（同文件只计一次）
	hit      bool
}

// defaultEvidenceBudget 缺省任务预算（引用数 + 总字节）。
func defaultEvidenceBudget() *evidenceBudget {
	return &evidenceBudget{maxRefs: maxEvidenceRefsPerTask, maxBytes: maxEvidenceBytesPerTask}
}

// allowRef 读取前记账：超引用数上限后拒绝后续 ref（已截断即持续拒绝）。
func (b *evidenceBudget) allowRef() bool {
	if b == nil {
		return true
	}
	if b.hit || (b.maxRefs > 0 && b.refs >= b.maxRefs) {
		b.hit = true
		return false
	}
	b.refs++
	return true
}

// addFile 记录文件字节（同 path 只计一次）；超总字节上限返回 false。
func (b *evidenceBudget) addFile(path string, lines []string) bool {
	if b == nil {
		return true
	}
	if b.hit {
		return false
	}
	if b.files == nil {
		b.files = map[string]int{}
	}
	if _, ok := b.files[path]; ok {
		return true
	}
	n := 0
	for _, l := range lines {
		n += len(l) + 1
	}
	b.files[path] = n
	b.bytes += n
	if b.maxBytes > 0 && b.bytes > b.maxBytes {
		b.hit = true
		return false
	}
	return true
}
