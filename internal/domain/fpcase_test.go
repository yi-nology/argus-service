package domain

import "testing"

// TestFPCasePatternProjection 仓无关投影（批 E §3.2 三组钉）：同模式跨仓
// 同 Pattern / 不同 org 不共享 / 归一不误伤（file:line·数字·引号剥离）。
func TestFPCasePatternProjection(t *testing.T) {
	// 1. 同模式跨仓同 Pattern（file 归一去目录层级 + comment 骨架同）
	p1 := FPCasePattern("gitea", "acme-group", "auth/login.go", "口令硬编码在 config.go:12 处理")
	p2 := FPCasePattern("gitea", "acme-group", "internal/auth/login.go", "口令硬编码在 settings.go:40 处理")
	if p1 != p2 {
		t.Fatalf("同 org 同模式跨仓路径应同 Pattern:\n%s\n%s", p1, p2)
	}

	// 2. org 轴不跨（不同组织不共享）
	p3 := FPCasePattern("gitea", "other-group", "auth/login.go", "口令硬编码在 config.go:12 处理")
	if p3 == p1 {
		t.Fatal("不同 org 不应同 Pattern（信任边界隔离）")
	}

	// 3. 不同模式不同 Pattern（评语骨架差异保留）
	p4 := FPCasePattern("gitea", "acme-group", "auth/login.go", "SQL 字符串拼接未参数化")
	if p4 == p1 {
		t.Fatal("不同评语不应同 Pattern")
	}

	// 归一不误伤：file:line 引用/数字/引号内容剥离，但关键词保留
	if normalizeFPComment("越权 at a.go:99 count=3") != normalizeFPComment("越权 at b.go:12 count=7") {
		t.Fatalf("file:line+数字应归一收敛: %q vs %q",
			normalizeFPComment("越权 at a.go:99 count=3"),
			normalizeFPComment("越权 at b.go:12 count=7"))
	}
	// 空面稳定
	if FPCasePattern("gitea", "acme", "", "") == "" {
		t.Fatal("空输入应稳定 hash 非空")
	}
}
