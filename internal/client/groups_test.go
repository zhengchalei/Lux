package client

import (
	"lux/internal/domain"
	"testing"
)

// groups_test.go verifies direct grouping never replaces roster identity or LCU priority.
// Supplemental service observations apply only to public members already on the board.

// TestMergeLiveGroupsPreservesPublicRoster reproduces hidden and conflicting supplemental membership.
// Positive client grouping remains authoritative and no additional account is inserted.
func TestMergeLiveGroupsPreservesPublicRoster(t *testing.T) {
	b := domain.Battle{Players: []domain.Combatant{{PUUID: "known", Group: "4", GroupSource: "LCU"}, {PUUID: "other"}, {PUUID: "hidden", Hidden: true}, {}}}
	mergeLiveGroups(&b, map[string]string{"known": "8", "other": "8", "hidden": "8", "intruder": "8", "": "8"})
	if b.Players[0].Group != "4" || b.Players[1].Group != "8" || b.Players[1].GroupSource != "SGP GSM" || b.Players[2].Group != "" || b.Players[3].Group != "" || len(b.Players) != 4 {
		t.Fatalf("bad merge %+v", b.Players)
	}
	for _, value := range []any{nil, 0, -1, "0"} {
		if participantGroup(map[string]any{"teamParticipantId": value}) != "" {
			t.Fatal("absent group admitted")
		}
	}
}
