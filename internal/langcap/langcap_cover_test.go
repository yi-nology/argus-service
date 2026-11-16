// langcap_cover_test.go —— 描述口径派生面补测（#36）：JoinLangs/LangsOf 的
// 去重、首见序与未知扩展名透出（新扩展名入集不静默丢的纪律钉）。
package langcap

import "testing"

func TestJoinLangsAndLangsOf(t *testing.T) {
	if got := JoinLangs([]string{".go", ".py", ".go"}); got != "Go/Python" {
		t.Fatalf("JoinLangs 去重失真: %q", got)
	}
	langs := LangsOf([]string{".ts", ".pyw", ".sh", ".未知ext"})
	want := []string{"TypeScript", "Python", "shell", ".未知ext"}
	if len(langs) != len(want) {
		t.Fatalf("LangsOf 长度失真: %v", langs)
	}
	for i, w := range want {
		if langs[i] != w {
			t.Fatalf("LangsOf[%d] = %q, want %q（首见序 + 未知透出）", i, langs[i], w)
		}
	}
	if LangsOf(nil) == nil || len(LangsOf(nil)) != 0 {
		t.Fatal("空集应空切片")
	}
}
