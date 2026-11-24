// reposettings_reply_test.go —— 回复控制四旋钮（v8.4.0）：Parse 校验 + Or 访问器。
package reposettings

import (
	"strings"
	"testing"
)

func TestParseReplyControlValid(t *testing.T) {
	s, err := Parse([]byte(`{
		"comment_throttle_minutes": 30,
		"findings_cap": 8,
		"severity_floor": "medium",
		"ocr_delegate_enabled": false
	}`))
	if err != nil {
		t.Fatalf("合法四旋钮应通过: %v", err)
	}
	if s.CommentThrottleMinutesOr(0) != 30 || s.FindingsCapOr(0) != 8 ||
		s.SeverityFloorOr("") != "medium" || s.OCRDelegateEnabledOr(true) != false {
		t.Fatalf("Or 访问器读值错: %+v", s)
	}
	nb, _ := s.Normalize()
	if !strings.Contains(string(nb), `"severity_floor":"medium"`) {
		t.Fatalf("Normalize 应保留新字段: %s", nb)
	}
}

func TestParseReplyControlRejects(t *testing.T) {
	for name, js := range map[string]string{
		"throttle 超上限": `{"comment_throttle_minutes": 1441}`,
		"throttle 零值":  `{"comment_throttle_minutes": 0}`,
		"cap 超上限":      `{"findings_cap": 51}`,
		"cap 负数":       `{"findings_cap": -1}`,
		"floor 词表外":    `{"severity_floor": "critical-high"}`,
		"floor 大写":     `{"severity_floor": "HIGH"}`,
		"未知字段防拼写错":     `{"comment_throtle_minutes": 30}`,
	} {
		if _, err := Parse([]byte(js)); err == nil {
			t.Errorf("%s 应拒绝: %s", name, js)
		}
	}
}

func TestReplyControlBoundary(t *testing.T) {
	s, err := Parse([]byte(`{"comment_throttle_minutes": 1440, "findings_cap": 50, "severity_floor": "low"}`))
	if err != nil {
		t.Fatalf("边界值应通过: %v", err)
	}
	// 未设置字段落默认
	if s.MonthlyTokenBudgetOr(500) != 500 || s.ReportTemplateOr("standard") != "standard" {
		t.Fatal("未设置字段应落调用方默认")
	}
	// nil receiver 安全
	var nilS *Settings
	if nilS.CommentThrottleMinutesOr(0) != 0 || nilS.SeverityFloorOr("") != "" || !nilS.OCRDelegateEnabledOr(true) {
		t.Fatal("nil receiver 应落默认")
	}
	// IsZero：只有新字段也算非零
	if (&Settings{SeverityFloor: strp("low")}).IsZero() {
		t.Fatal("仅 severity_floor 不应 IsZero")
	}
}

func strp(s string) *string { return &s }

// SeverityFloorVocab 导出词表 + FindingsCapOr nil 分支（ratchet 回基线补齐）。
func TestSeverityFloorVocabAndFindingsCapOr(t *testing.T) {
	vocab := SeverityFloorVocab()
	if len(vocab) != 4 || vocab[0] != "critical" || vocab[3] != "low" {
		t.Fatalf("词表应 critical..low 四档: %v", vocab)
	}
	// FindingsCapOr 三态：nil receiver / 未设置 / 显式值
	var nilS *Settings
	if nilS.FindingsCapOr(7) != 7 {
		t.Fatal("nil receiver 应落默认")
	}
	if (&Settings{}).FindingsCapOr(7) != 7 {
		t.Fatal("未设置应落默认")
	}
	if (&Settings{FindingsCap: intp(9)}).FindingsCapOr(7) != 9 {
		t.Fatal("显式值应生效")
	}
}

func intp(v int) *int { return &v }
