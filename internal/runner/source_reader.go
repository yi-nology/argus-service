package runner

// workcopy SourceReader 实现（Phase 2 Evidence v2）：证据核验的文件读取面。
// 走 workcopy 池 Ensure 短借语义（同 key 幂等 rc++，与 OCR/fixverifier 同池
// 纪律：每 ReadLines 借还平衡，不长占）；FSFence 围栏复用 fixverifier 导出的
// ResolveSafePath（仓库可携带指向沙箱外的 symlink，跟随读=越界读）。
//
// 资源预算与缓存（v7.5 #69）：单文件按字节/行数设限（超限返回
// plugin.ErrSourceFileTooLarge，由核验层降级 unverified 并标注，不误判
// invalid）；单次读取带 ctx 超时；同一任务内按 path 缓存结果（含错误），
// 同名文件只真正读一次——此前每条 finding 的每个 ref 都独立 Ensure/ReadFile/
// Release，大量引用时 O(refs) 次磁盘读取。

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/oagentkit/tools/workcopy"

	"github.com/yi-nology/argus/internal/fixverifier"
	"github.com/yi-nology/argus/internal/plugin"
)

// 缺省读取预算（#69）：单文件 2 MiB / 5 万行 / 单次读取 3s。
const (
	defaultMaxSourceFileBytes int64 = 2 << 20
	defaultMaxSourceFileLines       = 50000
	defaultSourceReadTimeout        = 3 * time.Second
)

// workcopySourceReader 绑定单一 PR 检出（key.HeadSHA=任务 head，构造方保证
// 与 task.HeadSHA 一致——HeadSHA() 非空是 plugin.SourceReader 的 verified
// 必要条件之一）。
type workcopySourceReader struct {
	ctx context.Context
	wc  *workcopy.Pool
	key workcopy.WorktreeKey

	// 预算（构造器填缺省；测试可注入小值钉住边界）。
	maxFileBytes int64
	maxFileLines int
	timeout      time.Duration

	// mu 同时守护 cache 与 stats；读取期间持锁——单任务核验是顺序路径，
	// 串行化换「同名文件只读一次」的硬保证（含并发调用去重）。
	mu    sync.Mutex
	cache map[string]sourceCacheEntry
	reads int   // 实际磁盘读取次数（缓存命中不增）
	bytes int64 // 实际读取字节数（含超限探测读）
}

// sourceCacheEntry 单文件读取结果缓存（错误也缓存：基础设施抖动下不重复
// 借还 workcopy/重读，避免 O(refs) 次失败重试放大延迟）。
type sourceCacheEntry struct {
	lines []string
	err   error
}

// newWorkcopySourceReader 构造绑定指定检出的读取面（缺省预算）。
func newWorkcopySourceReader(ctx context.Context, wc *workcopy.Pool, key workcopy.WorktreeKey) *workcopySourceReader {
	return &workcopySourceReader{
		ctx:          ctx,
		wc:           wc,
		key:          key,
		maxFileBytes: defaultMaxSourceFileBytes,
		maxFileLines: defaultMaxSourceFileLines,
		timeout:      defaultSourceReadTimeout,
		cache:        make(map[string]sourceCacheEntry),
	}
}

// HeadSHA 绑定检出版本（plugin.SourceReader 契约）。
func (r *workcopySourceReader) HeadSHA() string { return r.key.HeadSHA }

// ReadLines 短借 workcopy → 围栏解析 → 按预算读文件行；同 path 命中缓存。
// 错误链保持可分类：
//   - fs.ErrNotExist 透传（证据判 invalid）；
//   - plugin.ErrSourceFileTooLarge（超字节/行数预算）透传（证据降级 unverified）；
//   - 其余（凭证/网络/围栏/超时）由 verify 层按基础设施失败降级集合级。
func (r *workcopySourceReader) ReadLines(path string) ([]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if e, ok := r.cache[path]; ok {
		return e.lines, e.err
	}
	lines, err := r.readFile(path)
	if r.cache == nil {
		r.cache = make(map[string]sourceCacheEntry)
	}
	r.cache[path] = sourceCacheEntry{lines: lines, err: err}
	return lines, err
}

// readFile 实际读取（调用方持 r.mu）。ctx 贯穿 Ensure（网络/克隆段）并在
// 读取前后检查超时；磁盘读取本身受字节上限约束（LimitReader 多读 1 字节
// 探测越界），不会把超大文件整读进内存。
func (r *workcopySourceReader) readFile(path string) ([]string, error) {
	if r.wc == nil {
		return nil, errors.New("evidence source reader: workcopy pool is nil")
	}
	ctx := r.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	if r.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, r.timeout)
		defer cancel()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	dir, err := r.wc.Ensure(ctx, r.key)
	if err != nil {
		return nil, err
	}
	defer r.wc.Release(r.key)
	abs, err := fixverifier.ResolveSafePath(dir, path)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(abs)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if st, err := f.Stat(); err == nil && st.IsDir() {
		return nil, fmt.Errorf("%w: %s is a directory", plugin.ErrSourceFileTooLarge, path)
	}

	maxBytes := r.maxFileBytes
	if maxBytes <= 0 {
		maxBytes = defaultMaxSourceFileBytes
	}
	b, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	r.reads++
	r.bytes += int64(len(b))
	if err != nil {
		return nil, err
	}
	// 读取期间越过的超时也要暴露（LimitReader 不感知 ctx）。
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if int64(len(b)) > maxBytes {
		return nil, fmt.Errorf("%w: %s exceeds %d bytes", plugin.ErrSourceFileTooLarge, path, maxBytes)
	}
	lines := strings.Split(string(b), "\n")
	maxLines := r.maxFileLines
	if maxLines <= 0 {
		maxLines = defaultMaxSourceFileLines
	}
	if len(lines) > maxLines {
		return nil, fmt.Errorf("%w: %s exceeds %d lines", plugin.ErrSourceFileTooLarge, path, maxLines)
	}
	return lines, nil
}
