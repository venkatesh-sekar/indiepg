package pg

import (
	"strings"
	"testing"
)

func TestInjectHBARules_AddsBlockWhenAbsent(t *testing.T) {
	existing := "local   all   all   peer\nhost    all   all   127.0.0.1/32   scram-sha-256\n"
	updated, changed := injectHBARules(existing)
	if !changed {
		t.Fatal("expected changed=true for content without the managed block")
	}
	if !strings.Contains(updated, hbaMarkerBegin) || !strings.Contains(updated, hbaMarkerEnd) {
		t.Fatal("managed markers not present after injection")
	}
	// The trust rules for both panel roles must be present.
	for _, role := range []string{ReadOnlyRole, AdminRole} {
		if !strings.Contains(updated, "local   all   "+role+"   trust") {
			t.Fatalf("missing trust rule for %s", role)
		}
	}
	// The managed block must come BEFORE the original peer rule (first match wins).
	if strings.Index(updated, hbaMarkerBegin) > strings.Index(updated, "local   all   all   peer") {
		t.Fatal("managed block must precede the default peer rule")
	}
	// Original content must be preserved.
	if !strings.Contains(updated, "host    all   all   127.0.0.1/32   scram-sha-256") {
		t.Fatal("original pg_hba content was lost")
	}
}

func TestInjectHBARules_Idempotent(t *testing.T) {
	existing := "local   all   all   peer\n"
	once, changed1 := injectHBARules(existing)
	if !changed1 {
		t.Fatal("first injection should change content")
	}
	twice, changed2 := injectHBARules(once)
	if changed2 {
		t.Fatal("second injection should be a no-op")
	}
	if once != twice {
		t.Fatal("idempotent injection must return identical content")
	}
	if strings.Count(twice, hbaMarkerBegin) != 1 {
		t.Fatalf("managed block must appear exactly once, got %d", strings.Count(twice, hbaMarkerBegin))
	}
}

func TestInjectHBARules_EmptyInput(t *testing.T) {
	updated, changed := injectHBARules("")
	if !changed {
		t.Fatal("empty input should be changed")
	}
	if !strings.Contains(updated, hbaMarkerBegin) {
		t.Fatal("managed block should be added to empty input")
	}
}
