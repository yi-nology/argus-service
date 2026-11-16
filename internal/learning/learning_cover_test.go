// learning_cover_test.go —— 学习环模型补测（#36）：SampleText 回退与缺省阈值。
package learning

import "testing"

func TestSampleTextFallback(t *testing.T) {
	if got := (PatternState{PatternText: "原文", Pattern: "键"}).SampleText(); got != "原文" {
		t.Fatalf("PatternText 优先: %q", got)
	}
	if got := (PatternState{Pattern: "键"}).SampleText(); got != "键" {
		t.Fatalf("缺原文应回退键: %q", got)
	}
}

func TestDefaultConfig(t *testing.T) {
	c := DefaultConfig()
	if c.Enabled || c.MinSamples != 3 || c.InjectTopN != 10 {
		t.Fatalf("缺省阈值失真（design §3.7）: %+v", c)
	}
	if c.SoftFloor != 0.35 || c.HardFloor != 0.20 || c.InjectFloor != 0.35 {
		t.Fatalf("阈值档失真: %+v", c)
	}
}
