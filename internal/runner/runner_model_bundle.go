// runner_model_bundle.go —— 模型视图 bundle（#95 per-tenant LLM 消费面）。
//
// buildWorkflow 原直读 Runner 的八个模型字段（LLM/StageLLMs/Model/R3Model/
// FallbackModel/ReflectModel/ModelName/LLMLimiter）——并发多 worker 共享
// Runner，逐任务改字段不是选项。bundle 是执行期解析一次的只读视图：
//   - 默认视图 = 全局装配字段（sync.Once 惰性快照，单租户零变化）；
//   - 租户视图 = TenantProfiles.LLM 差量合成的独立模型栈（bootstrap 装配期
//     BuildModels 逐租户构建，map 查表 O(1)）。
// 插件 WorkflowFor/WorkflowFactory 无 ctx 形参（签名外溢面大），走默认视图——
// 租户插件路径的模型路由是残余面（#95 评论在案）。

package runner

import (
	"context"

	"github.com/cloudwego/eino/components/model"

	"github.com/oagentkit/llm"
	"github.com/oekit/concurrency/ratelimit"

	"github.com/yi-nology/argus/internal/tenant"
)

// ModelBundle 一次任务执行的模型只读视图。
type ModelBundle struct {
	LLM           llm.Generator            // 主生成链（Resilient/Client）
	StageLLMs     map[string]llm.Generator // R1/R2 阶段覆盖
	Model         model.BaseChatModel      // 单模型出口（LLM 为 nil 时）
	ModelName     string
	R3Model       model.BaseChatModel // R3 ReAct 覆盖（nil=回退 LLM.RawModel）
	FallbackModel model.BaseChatModel // ReAct 主路径 failover 备模型
	FallbackName  string
	ReflectModel  model.BaseChatModel // R3.5 反思覆盖
	LLMLimiter    ratelimit.Limiter
}

// TenantStacks 租户模型视图表（#95）：bootstrap 装配 TenantProfiles 里配了
// LLM 覆盖的租户；nil/缺租户 = 回落默认视图。
func (r *Runner) modelBundle(ctx context.Context) *ModelBundle {
	if tid := tenant.FromContext(ctx); tid != tenant.Default && len(r.TenantStacks) > 0 {
		if b, ok := r.TenantStacks[tid]; ok && b != nil {
			return b
		}
	}
	return r.defaultBundle()
}

// defaultBundle 全局默认视图惰性快照（一次构建，并发只读）。
func (r *Runner) defaultBundle() *ModelBundle {
	r.defBundleOnce.Do(func() {
		r.defBundle = &ModelBundle{
			LLM:           r.LLM,
			StageLLMs:     r.StageLLMs,
			Model:         r.Model,
			ModelName:     r.ModelName,
			R3Model:       r.R3Model,
			FallbackModel: r.FallbackModel,
			FallbackName:  r.FallbackName,
			ReflectModel:  r.ReflectModel,
			LLMLimiter:    r.LLMLimiter,
		}
	})
	return r.defBundle
}
