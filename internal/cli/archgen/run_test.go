package archgen

import "testing"

func TestModuleID(t *testing.T) {
	cases := map[string]string{
		"github.com/yi-nology/argus/internal/codesearch":           "codesearch",
		"github.com/yi-nology/argus/internal/workflow/requirement": "requirement",
		"github.com/yi-nology/argus/internal/plugin/adapter_acpx":  "adapter_acpx",
		"github.com/yi-nology/argus/internal/channel/mcpserver":    "mcpserver",
		"github.com/yi-nology/argus/internal/notify/octopus":       "octopus",
	}
	for in, want := range cases {
		if got := moduleID(in); got != want {
			t.Fatalf("moduleID(%q)=%q want %q", in, got, want)
		}
	}
}

func TestExtRoot(t *testing.T) {
	cases := map[string]string{
		"github.com/oekit/pkg/errors":           "github.com/oekit/pkg",
		"github.com/oagentkit/runtime/progress": "github.com/oagentkit/runtime",
		"github.com/oagentkit/textx/spill":      "github.com/oagentkit/textx",
		"gorm.io/gorm/clause":                   "gorm.io/gorm",
		"github.com/google/uuid":                "github.com/google/uuid",
	}
	for in, want := range cases {
		if got := extRoot(in); got != want {
			t.Fatalf("extRoot(%q)=%q want %q", in, got, want)
		}
	}
}

func TestDedup(t *testing.T) {
	got := dedup([]string{"a", "a", "b"})
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("got %v", got)
	}
}
