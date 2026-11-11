package client

import (
	"context"

	"lux/internal/domain"
	"lux/internal/history"
)

// draft_choices.go reads the personal ARAM preselection cards and distinguishes
// them from the shared replacement bench. The client publishes these cards at a
// separate lobby-team-builder endpoint rather than inside benchChampions. During
// BAN_PICK, personal cards can be selected only when the client also reports them
// as pickable; visible shared bench cards must wait for finalization instead.
//
// An unassigned player chooses through an active pick action, while an assigned
// player exchanges through the bench endpoint. Champion intentions do not count
// as assignments in this stage. Both rendering and manual command validation use
// this normalization, so an empty current champion remains a valid first-choice
// state without granting the same permission to skin changes or arbitrary picks.

// personalChoiceStage reports whether the session exposes its initial personal cards.
func personalChoiceStage(session map[string]any) bool {
	return truth(session["allowSubsetChampionPicks"]) && text(object(session["timer"])["phase"]) == "BAN_PICK"
}

// readPersonalChoices reads personal cards only during their client-defined stage.
// Missing optional data returns no cards, without borrowing the shared bench or
// the account's entire champion inventory as a synthetic personal choice list.
func readPersonalChoices(ctx context.Context, access history.Access, session map[string]any) []int {
	if !personalChoiceStage(session) {
		return nil
	}
	// Older or transitioning clients may lack this endpoint; preserve the room and
	// show no personal cards until a subsequent client observation supplies them.
	ids, _ := readIDs(ctx, access, "/lol-lobby-team-builder/champ-select/v1/subset-champion-list")
	return ids
}

// personalPickAction finds the local player's currently active uncompleted pick action.
// A missing action disables first selection. Completed, future, or other players'
// actions cannot authorize the initial personal champion choice.
func personalPickAction(session map[string]any) map[string]any {
	for _, action := range actionRows(session) {
		completed, observed := action["completed"].(bool)
		if number(action["actorCellId"]) == number(session["localPlayerCellId"]) && text(action["type"]) == "pick" && observed && !completed && truth(action["isInProgress"]) {
			return action
		}
	}
	return nil
}

// applyPersonalChoices separates visible personal cards from verified actionable choices.
// It runs only for random modes during the personal BAN_PICK stage. The actual
// local assignment overrides a hovered intention, allowing current zero to mean
// that the player's first pick action is still required. Missing availability
// observations retain visible cards but disable their commands rather than
// interpreting unknown pickability as permission. Shared bench cards remain
// visible through Room.Bench and cannot enter Available during this stage.
func applyPersonalChoices(room *domain.Room, session map[string]any, subset, picks, disabled []int, known bool) {
	room.CanSwap = false
	// Personal preselection treats championPickIntent as hovering only. Retaining that
	// intention as an assignment would incorrectly bypass the player's first pick action.
	for _, raw := range array(session["myTeam"]) {
		member := object(raw)
		if number(member["cellId"]) == number(session["localPlayerCellId"]) {
			room.Current = number(member["championId"])
			break
		}
	}
	room.CanChoose = !truth(session["isSpectating"]) && (room.Current > 0 || personalPickAction(session) != nil)
	for _, id := range subset {
		if id <= 0 || contains(room.Choices, id) {
			continue
		}
		room.Choices = append(room.Choices, id)
		if known && contains(picks, id) && !contains(disabled, id) && !contains(room.Banned, id) {
			room.Available = append(room.Available, id)
		}
	}
}
