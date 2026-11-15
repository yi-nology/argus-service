package plugin

import "testing"

// TestVisualBriefRoundtrip 视觉简报数据面（批 G）：Artifacts 反解 +
// 缺位/坏 JSON 返回 nil（渲染/消费面安全）。
func TestVisualBriefRoundtrip(t *testing.T) {
	if VisualBriefOf(map[string]string{}) != nil {
		t.Fatal("缺位应 nil")
	}
	if VisualBriefOf(map[string]string{ArtifactVisualBrief: "{bad"}) != nil {
		t.Fatal("坏 JSON 应 nil")
	}
	raw := `{"refs":[{"kind":"screenshot","ref":"docs/ui.png"}],` +
		`"text":{"docs/ui.png":"登录页 顶部导航"},"errors":{}}`
	vb := VisualBriefOf(map[string]string{ArtifactVisualBrief: raw})
	if vb == nil || len(vb.Refs) != 1 || vb.Refs[0].Kind != "screenshot" {
		t.Fatalf("反解错: %+v", vb)
	}
	if vb.Text["docs/ui.png"] == "" {
		t.Fatalf("文本缺: %+v", vb.Text)
	}
}
