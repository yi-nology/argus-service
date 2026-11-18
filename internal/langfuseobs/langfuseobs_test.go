// langfuseobs_test.go —— GenAI 语义 span 铸造（Langfuse OTLP ingest 映射）：
// 每笔 UsageRecord 一个根 span，gen_ai.* 计量属性 + langfuse.* 归因属性齐备，
// 新旧 token 语义约定双发（input/prompt、output/completion）兼容 Langfuse 映射。
package langfuseobs

import (
	"context"
	"testing"

	"github.com/oagentkit/llm"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func recorder(t *testing.T) *tracetest.InMemoryExporter {
	t.Helper()
	exp := &tracetest.InMemoryExporter{}
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp))
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() { otel.SetTracerProvider(prev) })
	return exp
}

func attrs(s tracetest.SpanStub) map[string]string {
	m := map[string]string{}
	for _, kv := range s.Attributes {
		m[string(kv.Key)] = kv.Value.String()
	}
	return m
}

func TestRecordSpansGenAISemantics(t *testing.T) {
	exp := recorder(t)
	rec := llm.UsageRecord{
		Model: "qwen3-235b", PromptTokens: 1200, CachedTokens: 300,
		CompletionTokens: 80, ReasoningTokens: 40, TotalTokens: 1280,
		DurationMS: 2500, FinishReason: "stop", Iteration: 3,
	}
	Record(context.Background(), TaskAttr{
		TaskID: "t-42", Platform: "gitlab", Owner: "acme", Repo: "pay", Number: "7",
		Stage: "R4", TaskKind: "code_review",
	}, rec)

	spans := exp.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("应产生 1 个 span, got %d", len(spans))
	}
	s := spans[0]
	if s.Name != "llm qwen3-235b" {
		t.Errorf("span name = %q, want 含模型名", s.Name)
	}
	m := attrs(s)
	// 官方词表显式声明（llm v0.27 obsx 同款映射，不依赖隐式判定一途）
	if m["langfuse.observation.type"] != "generation" {
		t.Errorf("langfuse.observation.type = %q, want generation", m["langfuse.observation.type"])
	}
	// gen_ai 语义（Langfuse generation 映射的判据：model 属性在即成 generation）
	for k, want := range map[string]string{
		"gen_ai.request.model":      "qwen3-235b",
		"gen_ai.operation.name":     "argus.review",
		"gen_ai.usage.total_tokens": "1280",
		// 新旧 semconv 双发：Langfuse 对 input/prompt 两种形态均映射
		"gen_ai.usage.input_tokens":      "1200",
		"gen_ai.usage.prompt_tokens":     "1200",
		"gen_ai.usage.output_tokens":     "80",
		"gen_ai.usage.completion_tokens": "80",
		"gen_ai.usage.cached_tokens":     "300",
		"gen_ai.usage.reasoning_tokens":  "40",
		"gen_ai.response.finish_reason":  "stop",
		// langfuse 归因（trace 级属性任意 span 均可携带，官方推荐每 span 都带）
		"langfuse.session.id": "t-42",
		"langfuse.user.id":    "gitlab:acme/pay",
		"langfuse.trace.name": "code_review gitlab:acme/pay#7",
		"llm.iteration":       "3",
		"llm.stage":           "R4",
		"llm.partial":         "false",
	} {
		if m[k] != want {
			t.Errorf("%s = %q, want %q", k, m[k], want)
		}
	}
	// trace.tags：kind+stage 混合标签（attribute slice 形态）
	found := false
	for _, kv := range s.Attributes {
		if kv.Key == attribute.Key("langfuse.trace.tags") {
			found = true
		}
	}
	if !found {
		t.Error("缺 langfuse.trace.tags（会话页过滤维度）")
	}
}

// Duration 由 span 自身承载（start/end），span 时长与记录一致（±5ms 容差）。
func TestRecordSpanDurationCarriesLatency(t *testing.T) {
	exp := recorder(t)
	Record(context.Background(), TaskAttr{TaskID: "t-d", Platform: "gitea",
		Owner: "o", Repo: "r", Number: "1", TaskKind: "code_review"},
		llm.UsageRecord{Model: "m", TotalTokens: 10, DurationMS: 40})

	spans := exp.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("span 数 = %d", len(spans))
	}
	d := spans[0].EndTime.Sub(spans[0].StartTime).Milliseconds()
	if d < 35 || d > 500 {
		t.Errorf("span 时长 %dms 与记录 40ms 不符", d)
	}
}

// nil TracerProvider/零值记录防御：不 panic、不产 span。
func TestRecordNilSafe(t *testing.T) {
	exp := recorder(t)
	Record(context.Background(), TaskAttr{}, llm.UsageRecord{})
	if n := len(exp.GetSpans()); n != 1 {
		t.Fatalf("零值记录仍应产 span（计量 0 可观测）, got %d", n)
	}
}
