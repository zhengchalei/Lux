package publicdata

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"lux/internal/domain"
)

// sources_test.go checks public source validation and adjustment interpretation.
// Fixtures exercise the same parsing path as automatic refresh without making
// network requests, and protect unknown-data behavior as well as incoming damage's
// reversed benefit direction when the Go implementation replaces the Rust version.
//
// Independent source updates deliberately carry conflicting values for the same
// champion. Each update must preserve the other mode's full observation, including
// its source label and unknown state. Advancing a Mayhem patch invalidates only
// that mode's old adjustments; ordinary balance remains untouched. This prevents
// a successful refresh from silently presenting another rule set's strength when
// one provider has not returned data yet.

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

// TestProviderUpdatesKeepModesIndependent refreshes contradictory observations for one champion.
// Updating either source must preserve the other source's rate, adjustments, and provenance.
func TestProviderUpdatesKeepModesIndependent(t *testing.T) {
	aramRate, mayhemRate := 41.0, 63.0
	aram := domain.Statistics{Tier: "T4", WinRate: &aramRate, Samples: 100, Source: "ordinary fixture", Adjustments: []domain.Adjustment{{Label: "造成伤害", Display: "-5%", Helpful: false}}}
	mayhem := domain.Statistics{Tier: "S", WinRate: &mayhemRate, Samples: 200, Source: "RESG · old · fixture", Adjustments: []domain.Adjustment{{Label: "造成伤害", Display: "+10%", Helpful: true}}}
	catalog := map[int]domain.Champion{99: {ID: 99, Aram: aram, Mayhem: mayhem}}
	applyResg(catalog, []document{{"id": float64(99), "title": "拉克丝", "wr": .72, "tier": "S+", "tm": float64(300)}}, "new", "fixture")
	if !reflect.DeepEqual(catalog[99].Aram, aram) || *catalog[99].Mayhem.WinRate != 72 || catalog[99].Mayhem.Adjustments != nil {
		t.Fatalf("Mayhem refresh mixed sources or retained old patch adjustments: %+v", catalog[99])
	}
	updatedMayhem := catalog[99].Mayhem
	applyAram(catalog, []document{{"id": float64(99), "average_stats": map[string]any{"win_rate": .39, "tier": float64(5), "play": float64(400)}}}, "ordinary", "fixture")
	var balance document
	_ = json.Unmarshal([]byte(`{"champion_id":99,"damage_dealt":95,"damage_taken":105,"healing":100,"shield_amount":100,"attack_speed":100,"cooldown_reduction":0,"tenacity":0,"energy_regen":100}`), &balance)
	applyBalance(catalog, []document{balance}, "fixture")
	if !reflect.DeepEqual(catalog[99].Mayhem, updatedMayhem) || *catalog[99].Aram.WinRate != 39 || len(catalog[99].Aram.Adjustments) != 2 {
		t.Fatalf("ordinary refresh mixed modes: %+v", catalog[99])
	}
}

// TestSingleProviderDoesNotInventOtherMode checks missing observations after one provider succeeds.
// Ordinary rates and adjustments must remain unknown when only Mayhem has been received, and conversely.
func TestSingleProviderDoesNotInventOtherMode(t *testing.T) {
	catalog := map[int]domain.Champion{}
	applyResg(catalog, []document{{"id": float64(99), "wr": .6}}, "fixture", "fixture")
	if catalog[99].Aram.WinRate != nil || catalog[99].Aram.Adjustments != nil {
		t.Fatal("Mayhem data supplied missing ordinary observations")
	}
	catalog = map[int]domain.Champion{99: {ID: 99}}
	applyAram(catalog, []document{{"id": float64(99), "average_stats": map[string]any{"win_rate": .4}}}, "fixture", "fixture")
	if catalog[99].Mayhem.WinRate != nil || catalog[99].Mayhem.Adjustments != nil {
		t.Fatal("ordinary data supplied missing Mayhem observations")
	}
}
