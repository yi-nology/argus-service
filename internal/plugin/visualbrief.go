package plugin

// 视觉简报数据面（批 G spec §2.1）：visual_check 任务的截图/设计稿预处理
// 产物——OCR/多模态转结构化文本挂 Artifacts，R4 消费。输入通道
// （webhook/TaskAPI visual_refs wire 面）留部署批（IDL 链），本文件先落
// 载荷形态与常量。

import "encoding/json"

// ArtifactVisualBrief 视觉简报 Artifacts 键。
const ArtifactVisualBrief = "visual_brief"

// VisualRef 视觉输入引用（spec §2.1）：kind=screenshot|design；ref=URL 或
// 仓内路径。数量/尺寸上限由消费侧配置（spec §4 风险）。
type VisualRef struct {
	Kind string `json:"kind"` // screenshot | design
	Ref  string `json:"ref"`
}

// VisualBrief 预处理产物：refs 解析+每源文本化结果（best-effort——失败源
// 记 Error 不阻断，与 OCR delegate 降级纪律同源）。
type VisualBrief struct {
	Refs []VisualRef       `json:"refs"`
	Text map[string]string `json:"text,omitempty"`   // ref → OCR/多模态文本
	Errs map[string]string `json:"errors,omitempty"` // ref → 降级原因
}

// VisualBriefOf 从 Artifacts 反解；缺位/坏 JSON 返回 nil。
func VisualBriefOf(artifacts map[string]string) *VisualBrief {
	raw, ok := artifacts[ArtifactVisualBrief]
	if !ok || raw == "" {
		return nil
	}
	var vb VisualBrief
	if err := json.Unmarshal([]byte(raw), &vb); err != nil {
		return nil
	}
	return &vb
}
