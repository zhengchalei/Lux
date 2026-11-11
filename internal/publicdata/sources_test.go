package publicdata

import (
	"encoding/json"
	"strings"
	"testing"
)

// sources_test.go checks public source validation and adjustment interpretation.
// Fixtures exercise the same parsing path as automatic refresh without making
// network requests, and protect unknown-data behavior as well as incoming damage's
// reversed benefit direction when the Go implementation replaces the Rust version.

// TestBundledSources loads all public snapshots and verifies separate gameplay observations.
// The catalog must contain the expected bundled champions without inventing player records.
func TestBundledSources(t *testing.T) {
	s := &Service{}
	db := s.Database()
	if len(db.Champions) != 173 {
		t.Fatalf("catalog size %d", len(db.Champions))
	}
	c := db.Champion(99)
	if c.Name != "拉克丝" || c.Aram.WinRate == nil || c.Mayhem.WinRate == nil {
		t.Fatalf("missing independent Lux source observations: %#v", c)
	}
	if !strings.Contains(c.Mayhem.Source, "RESG") || !strings.Contains(c.Aram.Source, "OP.GG") {
		t.Fatal("mode sources were mixed")
	}
}

// TestModulesRejectExecutableStatements accepts static JSON exports and rejects executable payloads.
// This protects provider refresh from evaluating downloaded JavaScript when parsing public observations.
func TestModulesRejectExecutableStatements(t *testing.T) {
	var output any
	if DecodeModule([]byte("export default {\"items\": []};"), &output) != nil {
		t.Fatal("static export rejected")
	}
	if DecodeModule([]byte("export default {}; alert('bad')"), &output) == nil {
		t.Fatal("executable suffix accepted")
	}
	if DecodeModule([]byte("globalThis.data={}"), &output) == nil {
		t.Fatal("statement accepted")
	}
}

// TestAdjustmentDirectionPreservesUnknown verifies absolute and signed incoming-damage source values.
// Missing observations remain nil while explicitly empty observations remain a known empty list.
func TestAdjustmentDirectionPreservesUnknown(t *testing.T) {
	if resgAdjustments(nil) != nil {
		t.Fatal("missing balance became observed empty")
	}
	if resgAdjustments(document{}) == nil {
		t.Fatal("observed empty balance became unknown")
	}
	for _, input := range []document{{"承受伤害": "105%"}, {"承受伤害": "+5%"}} {
		values := resgAdjustments(input)
		if len(values) != 1 || values[0].Helpful || values[0].Display != "+5.0%" {
			t.Fatalf("incoming damage buff inverted: %#v", values)
		}
	}
}

// TestValidationRejectsMissingFields rejects incomplete ordinary balance rows before publication.
// A legitimate baseline row is accepted, preserving the distinction between neutral and unknown adjustments.
func TestValidationRejectsMissingFields(t *testing.T) {
	var row document
	_ = json.Unmarshal([]byte(`{"champion_id":99,"damage_dealt":100,"damage_taken":100,"healing":100,"shield_amount":100,"attack_speed":100,"cooldown_reduction":0,"tenacity":0,"energy_regen":100}`), &row)
	if !validRows([]document{row}, "balance") {
		t.Fatal("complete baseline rejected")
	}
	delete(row, "damage_taken")
	if validRows([]document{row}, "balance") {
		t.Fatal("missing incoming field accepted")
	}
}
