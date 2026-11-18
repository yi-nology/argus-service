// Package langfuseobs LLM 调用的 GenAI 语义 span 铸造（Langfuse OTLP ingest）。
//
// 数据通路：oagentkit/llm 的 UsageHandler（全任务唯一用量出口）→ 本包铸
// gen_ai.* 语义 span → 既有 OTel OTLP 管线（ARGUS_OTEL_ENDPOINT，oekit/tracing）
// → Langfuse `/api/public/otel`（Basic auth 经 ARGUS_OTEL_HEADERS 透传）。
// Langfuse 对带 `gen_ai.request.model` 的 span 自动映射为 generation 观测；
// `langfuse.session.id`=task_id 使同一任务的多次调用聚合进一个会话视图。
//
// 边界（刻意不做）：
//   - 不发 prompt/completion 文本（gen_ai.prompt/completion）——体积大且涉
//     评论值面 PII 纪律（gak pii 同款顾虑），第一版只发计量与归因；
//   - 不引 Langfuse SDK（Go 无官方 SDK）——OTLP 是官方一等公民通路；
//   - token 属性新旧 semconv 双发（input/prompt、output/completion）——
//     Langfuse 对两种形态均映射，双发兼容不挑版本。
package langfuseobs

import (
	"context"
	"time"

	"github.com/oagentkit/llm"
	"go.opentelemetry.io/otel"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// tracerName span 归属的 instrumentation 名。
const tracerName = "argus.langfuse"

// TaskAttr 任务归因坐标（runner usage sink 装配点在手的字段）。
type TaskAttr struct {
	TaskID   string
	Platform string
	Owner    string
	Repo     string
	Number   string
	Stage    string // R1/R2/R3/.../Reviewer（rec.Labels 里的 stage 语义）
	TaskKind string // code_review / requirement_compliance / ...
}

// tracer 全局 tracer（TracerProvider 由 initOTel 装配；未配置 OTel 时为
// no-op tracer，Record 零开销直通）。
func tracer() trace.Tracer { return otel.Tracer(tracerName) }

// now 可注入时钟（测试确定性；nil = time.Now）。
var now = time.Now

// Record 铸一笔 LLM 调用的 GenAI 语义根 span（全局 tracer：TracerProvider 由
// initOTel 装配，未配置 OTel 时 no-op 零开销；测试经 otel.SetTracerProvider 注入）。
// UsageRecord 无父子 ctx（sink 回调无 ctx），每笔独立 trace——任务聚合靠
// langfuse.session.id 属性，这是 Langfuse 官方的会话分组机制。Duration 由
// span 自身 start/end 承载（由 DurationMS 反推，相对当前时刻）。ctx 为 nil 安全。
func Record(ctx context.Context, task TaskAttr, rec llm.UsageRecord) {
	if ctx == nil {
		ctx = context.Background()
	}
	tr := tracer()
	name := "llm " + rec.Model
	if rec.Model == "" {
		name = "llm"
	}
	end := now()
	start := end.Add(-spanDur(rec))
	_, span := tr.Start(ctx, name,
		trace.WithTimestamp(start),
		trace.WithNewRoot()) // 独立 trace：无 parent、不继承采样
	defer span.End(trace.WithTimestamp(end))

	userID := task.Platform + ":" + task.Owner + "/" + task.Repo
	traceName := "argus llm"
	if task.TaskKind != "" || task.Platform != "" {
		traceName = task.TaskKind + " " + userID
		if task.Number != "" {
			traceName += "#" + task.Number
		}
	}
	attrs := []attribute.KeyValue{
		// 官方词表显式声明 generation 观测（oagentkit/llm v0.27 obsx 同款映射），
		// 不依赖 gen_ai.request.model 的隐式判定一途。
		attribute.String("langfuse.observation.type", "generation"),
		attribute.String("gen_ai.operation.name", "argus.review"),
		attribute.String("gen_ai.request.model", rec.Model),
		attribute.Int("gen_ai.usage.total_tokens", rec.TotalTokens),
		attribute.Int("gen_ai.usage.input_tokens", rec.PromptTokens),
		attribute.Int("gen_ai.usage.prompt_tokens", rec.PromptTokens),
		attribute.Int("gen_ai.usage.output_tokens", rec.CompletionTokens),
		attribute.Int("gen_ai.usage.completion_tokens", rec.CompletionTokens),
		attribute.Int("gen_ai.usage.cached_tokens", rec.CachedTokens),
		attribute.Int("gen_ai.usage.reasoning_tokens", rec.ReasoningTokens),
		attribute.String("gen_ai.response.finish_reason", rec.FinishReason),
		// Langfuse 归因（trace 级属性官方建议每个 span 都带）
		attribute.String("langfuse.session.id", task.TaskID),
		attribute.String("langfuse.user.id", userID),
		attribute.String("langfuse.trace.name", traceName),
		attribute.StringSlice("langfuse.trace.tags", traceTags(task)),
		// 领域补充（argus 侧过滤维度）
		attribute.Int64("llm.iteration", rec.Iteration),
		attribute.Bool("llm.partial", rec.Partial),
	}
	if task.Stage != "" {
		attrs = append(attrs, attribute.String("llm.stage", task.Stage))
	}
	span.SetAttributes(attrs...)
}

// traceTags 会话页过滤维度：任务类型 + 阶段 + 平台。
func traceTags(task TaskAttr) []string {
	tags := make([]string, 0, 3)
	if task.TaskKind != "" {
		tags = append(tags, task.TaskKind)
	}
	if task.Stage != "" {
		tags = append(tags, task.Stage)
	}
	if task.Platform != "" {
		tags = append(tags, task.Platform)
	}
	return tags
}

// spanDur DurationMS → time.Duration（<=0 记 1ms，保 span 形态合法）。
func spanDur(rec llm.UsageRecord) time.Duration {
	if rec.DurationMS <= 0 {
		return time.Millisecond
	}
	return time.Duration(rec.DurationMS) * time.Millisecond
}
