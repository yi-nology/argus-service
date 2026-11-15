package runner

// workcopy SourceReader 实现（Phase 2 Evidence v2）：证据核验的文件读取面。
// 走 workcopy 池 Ensure 短借语义（同 key 幂等 rc++，与 OCR/fixverifier 同池
// 纪律：每 ReadLines 借还平衡，不长占）；FSFence 围栏复用 fixverifier 导出的
// ResolveSafePath（仓库可携带指向沙箱外的 symlink，跟随读=越界读）。

import (
	"context"
	"os"
	"strings"

	"github.com/oagentkit/tools/workcopy"

	"github.com/yi-nology/argus/internal/fixverifier"
)

// workcopySourceReader 绑定单一 PR 检出（key.HeadSHA=任务 head，构造方保证
// 与 task.HeadSHA 一致——HeadSHA() 非空即具备 plugin.SourceReader 的 verified
// 资格）。
type workcopySourceReader struct {
	ctx context.Context
	wc  *workcopy.Pool
	key workcopy.WorktreeKey
}

// HeadSHA 绑定检出版本（plugin.SourceReader 契约）。
func (r *workcopySourceReader) HeadSHA() string { return r.key.HeadSHA }

// ReadLines 短借 workcopy → 围栏解析 → 读文件行。错误链保持可分类：
// fs.ErrNotExist 透传（证据判 invalid），其余（凭证/网络/围栏）由 verify 层
// 按基础设施失败降级集合级（不冤枉证据）。
func (r *workcopySourceReader) ReadLines(path string) ([]string, error) {
	dir, err := r.wc.Ensure(r.ctx, r.key)
	if err != nil {
		return nil, err
	}
	defer r.wc.Release(r.key)
	abs, err := fixverifier.ResolveSafePath(dir, path)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(abs)
	if err != nil {
		return nil, err
	}
	return strings.Split(string(b), "\n"), nil
}
