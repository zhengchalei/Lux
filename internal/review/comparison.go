package review

import (
	"errors"
	"fmt"

	"lux/internal/domain"
)

// comparison.go derives phase comparisons solely from observed sample boundaries.
// Three-minute targets select the next available frame rather than interpolating
// missing observations. Nonoverlapping event ranges count each recorded kill once,
// while unknown boundaries leave their gold and minion comparisons unavailable.
//
// Economic trend labels describe relative gold changes, not tactical correctness.
// A larger lead can coexist with a bad decision, and a support's lower income
// does not establish poor play. Those contextual judgments belong to evidence-linked
// coaching, whose inputs retain exact boundaries, sampled positions, and event
// participation without inferring that an unlisted player missed an objective.

// difference returns a signed observation only when both measurements are available.
// A nil input leaves the comparison unknown, allowing both phase totals and trend
// calculations to preserve gaps instead of inventing a missing boundary value.
func difference(a, b *int) *int {
	if a == nil || b == nil {
		return nil
	}
	v := *a - *b
	return &v
}

// Stats finds a recorded participant sample without manufacturing absent measurements.
// An absent slot retains its requested identifier with nil metrics, so downstream
// comparisons can distinguish missing observations from an actually observed zero.
func Stats(frame domain.TimelineFrame, id int) domain.TimelineStats {
	for _, stats := range frame.Players {
		if stats.ID == id {
			return stats
		}
	}
	return domain.TimelineStats{ID: id}
}

// eventKDA counts only explicitly recorded combat participation for one interval.
// Objective events do not establish combat assists, and duplicate assist entries
// count once for a participant. The caller supplies nonoverlapping interval events.
func eventKDA(events []domain.TimelineEvent, id int) [3]int {
	var result [3]int
	for _, e := range events {
		if e.Kind != "CHAMPION_KILL" {
			continue
		}
		if e.Actor == id {
			result[0]++
		}
		if e.Target == id {
			result[1]++
		}
		for _, assist := range e.Assists {
			if assist == id {
				result[2]++
				break
			}
		}
	}
	return result
}

// Compare prepares observed three-minute phases for a selected opposing participant.
// Zero requests the unique explicit-role suggestion or retains an unpaired review.
// Friendly and unknown slots fail before a comparison is constructed. Every phase
// spans actual frames, and events on a shared boundary belong to its preceding
// phase. Missing samples never become zero-valued economy gains or minion differences.
// Trend colors describe only relative income, leaving contextual tactical judgments
// to the coach rather than classifying every economic loss as a decision error.
func Compare(t domain.Timeline, opponent int) (domain.TimelineReview, error) {
	result := domain.TimelineReview{Timeline: t, Opponent: opponent, Windows: []domain.ReviewWindow{}}
	if opponent == 0 {
		opponent = t.SuggestedOpponent
		result.Opponent = opponent
	}
	ownTeam, enemyTeam := 0, 0
	for _, p := range t.Players {
		if p.ID == t.OwnID {
			ownTeam = p.Team
		}
		if p.ID == opponent {
			enemyTeam = p.Team
		}
	}
	if opponent != 0 && (enemyTeam == 0 || enemyTeam == ownTeam) {
		return result, errors.New("请选择本局的敌方选手")
	}
	if len(t.Frames) < 2 {
		return result, errors.New("时间线没有可对比的采样")
	}
	start := 0
	for end := 1; end < len(t.Frames); end++ {
		// A three-minute target keeps review cards readable; irregular client samples retain their actual boundary rather than assuming exact clock times.
		if t.Frames[end].At-t.Frames[start].At < 180000 && end != len(t.Frames)-1 {
			continue
		}
		w := domain.ReviewWindow{ID: fmt.Sprintf("W%d", len(result.Windows)+1), Start: t.Frames[start].At, End: t.Frames[end].At, Tone: "neutral", OwnStart: Stats(t.Frames[start], t.OwnID), OwnEnd: Stats(t.Frames[end], t.OwnID), OtherStart: Stats(t.Frames[start], opponent), OtherEnd: Stats(t.Frames[end], opponent), Events: []domain.TimelineEvent{}}
		w.GoldDiff = difference(w.OwnEnd.Gold, w.OtherEnd.Gold)
		w.CSDiff = difference(w.OwnEnd.CS, w.OtherEnd.CS)
		w.GoldTrend = difference(w.GoldDiff, difference(w.OwnStart.Gold, w.OtherStart.Gold))
		// A 300-gold change is only a visible economic-trend marker; it never labels a player's tactical decision as correct or incorrect.
		if w.GoldTrend != nil {
			if *w.GoldTrend >= 300 {
				w.Tone = "gain"
			}
			if *w.GoldTrend <= -300 {
				w.Tone = "loss"
			}
		}
		for _, e := range t.Events {
			if (e.At > w.Start || start == 0 && e.At == w.Start) && e.At <= w.End {
				w.Events = append(w.Events, e)
			}
		}
		w.OwnKDA = eventKDA(w.Events, t.OwnID)
		w.OtherKDA = eventKDA(w.Events, opponent)
		result.Windows = append(result.Windows, w)
		start = end
	}
	return result, nil
}
