// service_taskapi_suppress.go —— 误报抑制写面（自 service_taskapi.go 拆出）。

package service

import (
	"context"

	argus "github.com/yi-nology/argus/biz/model/argus"
	"github.com/yi-nology/argus/internal/domain"
)

// SuppressFinding 误报反馈（🔒，设计 §3.3 反馈通道最小入口）：
// 服务端按 Merger 同一指纹函数计算（file + 规范化评论文本哈希，行号不入指纹），
// 置 suppressed 后每轮报告自动剔除该意见。下一轮审查起生效（总结评论不即时重渲）。
func (s *Service) SuppressFinding(ctx context.Context, req *argus.SuppressFindingRequest) (*argus.SuppressFindingResponse, int) {
	return suppressFinding(s, ctx, req)
}

// suppressTarget SuppressFinding 两形态解析出的目标身份与处置状态。
type suppressTarget struct {
	fp              string
	file            string
	comment         string
	pluginName      string
	matched         bool
	suppressDone    bool
	resolvedFinding domain.Finding // 模式一解析出的入库原始身份（模式二为零值，仍用请求身份）
}

// suppressResolve 抑制目标解析：模式一（fp 短码直达）/ 模式二（file+原文近重复）
// 二选一，解析期即定的终态以 resp 非 nil 透出（未匹配 200 / 形态非法 400 /
// 近重复检索失败 500）；否则返回目标身份交调用方继续抑制回写。
func (s *Service) suppressResolve(ctx context.Context, req *argus.SuppressFindingRequest,
	platform string) (suppressTarget, *argus.SuppressFindingResponse, int) {
	return suppressResolve(s, ctx, req, platform)
}
