package main

import (
	"strings"
	"testing"

	"github.com/yi-nology/goagent-kernel/archcheck"
)

func TestReplaceBlock(t *testing.T) {
	in := "a\n<!-- BEGIN POLICY -->\nOLD\n<!-- END POLICY -->\nz\n"
	out, err := replaceBlock(in, beginMarker, endMarker, "NEW")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "NEW") || strings.Contains(out, "OLD") {
		t.Fatalf("unexpected: %q", out)
	}
	if !strings.HasPrefix(out, "a\n<!-- BEGIN POLICY -->\n") || !strings.HasSuffix(out, "<!-- END POLICY -->\nz\n") {
		t.Fatalf("markers not preserved: %q", out)
	}
}

func TestReplaceBlockMissing(t *testing.T) {
	if _, err := replaceBlock("no markers", beginMarker, endMarker, "x"); err == nil {
		t.Fatal("missing markers must fail")
	}
	if _, err := replaceBlock("<!-- END POLICY -->"+beginMarker, beginMarker, endMarker, "x"); err == nil {
		t.Fatal("reversed markers must fail")
	}
}

func TestRenderTable(t *testing.T) {
	got := renderTable([]archcheck.Module{
		{ID: "b", Roots: []string{"internal/b"}, Requires: []string{"a"}},
		{ID: "a", Roots: []string{"internal/a"}, Requires: nil, Notes: "L0"},
	})
	if !strings.Contains(got, "| internal/a | （无） | L0 |") {
		t.Fatalf("unexpected table: %s", got)
	}
	if strings.Index(got, "internal/a") > strings.Index(got, "internal/b") {
		t.Fatalf("not sorted by root: %s", got)
	}
}
