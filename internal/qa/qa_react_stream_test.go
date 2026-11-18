package qa

import (
	"context"
	"strings"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"github.com/yi-nology/argus/internal/plugin"
)

// streamFakeModel 复用 fakeModel 的 Generate 队列语义，Stream 返回合成分片。
type streamFakeModel struct {
	fakeModel
	chunks []*schema.Message
}

func (f *streamFakeModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return schema.StreamReaderFromArray(f.chunks), nil
}

// DeltaSink nil（回滚开关）：react 仍走整段 Generate，不触 Stream。
func TestReact_NoDeltaSinkUsesGenerate(t *testing.T) {
	m := &fakeModel{contents: []string{"答案"}}
	e, _, _ := newTestEngine(t, m)
	ans, err := e.react(context.Background(), "gitea", "acme", "app", "7", "问题", "", "")
	if err != nil {
		t.Fatalf("react: %v", err)
	}
	if ans != "答案" {
		t.Fatalf("answer = %q", ans)
	}
	if m.callCount() == 0 {
		t.Fatal("DeltaSink nil 应走 Generate")
	}
}

// DeltaSink 非 nil：react 走 agentrun 原生流式，增量与权威快照经宿主出口。
func TestReact_DeltaSinkStreams(t *testing.T) {
	big := strings.Repeat("a", 250)
	m := &streamFakeModel{chunks: []*schema.Message{
		{Role: schema.Assistant, Content: big},
		{Role: schema.Assistant, Content: "b"},
	}}
	e, _, _ := newTestEngine(t, m)
	var deltas []plugin.AgentDelta
	e.DeltaSink = func(_ context.Context, platform, owner, repo, number string, d plugin.AgentDelta) {
		if platform != "gitea" || owner != "acme" || repo != "app" || number != "7" {
			t.Errorf("DeltaSink 身份参数不符: %s/%s/%s#%s", platform, owner, repo, number)
		}
		deltas = append(deltas, d)
	}
	ans, err := e.react(context.Background(), "gitea", "acme", "app", "7", "问题", "", "")
	if err != nil {
		t.Fatalf("react: %v", err)
	}
	if ans != big+"b" {
		t.Fatalf("answer 拼接失真: %q", ans)
	}
	var incremental, final bool
	for _, d := range deltas {
		if d.Phase == "text" && !d.Final && d.Seq > 0 {
			incremental = true
		}
		if d.Phase == "text" && d.Final && d.Text == big+"b" {
			final = true
		}
	}
	if !incremental || !final {
		t.Fatalf("应有 text 增量与最终快照: %+v", deltas)
	}
}
