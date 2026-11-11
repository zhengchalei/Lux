package domain

import "testing"

// participant_test.go protects account identity mapping in both match adapters.
// Reordered LCU identity arrays and flat SGP Riot ID fields must retain the
// correct combat slot, while missing or explicitly hidden identities remain
// unqueryable instead of borrowing another participant's account or champion name.

// TestParticipantIdentityMapping separates same-name accounts with reordered identity slots.
// Both adapters preserve exact public PUUIDs and complete tags alongside their own combat values, including a participant without queryable identity.
func TestParticipantIdentityMapping(t *testing.T) {
	game := map[string]any{"gameId": 42, "queueId": 420, "gameDuration": 1200,
		"participantIdentities": []any{
			map[string]any{"participantId": 2, "player": map[string]any{"puuid": "second", "gameName": "同名", "tagLine": "200"}},
			map[string]any{"participantId": 1, "player": map[string]any{"puuid": "first", "gameName": "同名", "tagLine": "100"}},
		},
		"participants": []any{
			map[string]any{"participantId": 1, "championId": 99, "stats": map[string]any{"win": true, "kills": 3}},
			map[string]any{"participantId": 2, "championId": 103, "stats": map[string]any{"win": false, "kills": 9}},
			map[string]any{"participantId": 3, "championId": 22, "stats": map[string]any{"win": true}},
		}}
	m, ok := NormalizeLCU(game, "first", Database{})
	if !ok || m.Players[0].PUUID != "first" || m.Players[0].Name != "同名#100" || m.Players[1].Name != "同名#200" || m.Players[1].Kills != 9 || m.Players[2].PUUID != "" {
		t.Fatalf("LCU identity joined by order rather than slot: %+v", m.Players)
	}
	flat := map[string]any{"gameId": 43, "participants": []any{
		map[string]any{"puuid": "first", "riotIdGameName": "同名", "riotIdTagline": "100", "win": true, "championId": 99},
		map[string]any{"puuid": "second", "riotIdGameName": "同名", "riotIdTagline": "200", "win": false, "championId": 103},
		map[string]any{"puuid": "hidden", "gameName": "不可显示", "nameVisibilityType": "HIDDEN"},
	}}
	m, ok = NormalizeSGP(flat, "first", Database{})
	if !ok || m.Players[1].PUUID != "second" || m.Players[1].Name != "同名#200" || m.Players[2].PUUID != "" || m.Players[2].Name != "身份未公开" {
		t.Fatalf("SGP account metadata lost visibility or tag: %+v", m.Players)
	}
	game["participantIdentities"] = append(Array(game["participantIdentities"]), map[string]any{"participantId": 2, "player": map[string]any{"puuid": "conflict"}})
	m, _ = NormalizeLCU(game, "first", Database{})
	if m.Players[1].PUUID != "" {
		t.Fatal("ambiguous identity slot became queryable")
	}
}
