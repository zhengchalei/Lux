package domain

import "math"

// signals.go derives explainable Akari-style player observations from normalized
// matches. Summaries use the same competitive and mode eligibility as displayed
// win rates. Performance scoring requires every consumed metric, so absent vision
// or farm cannot silently lower a player's score or create a definitive label.
//
// Early-gank observations retain actual event times and distinguish observed zero
// deaths from an unreadable timeline. Only non-jungler samples with an identified
// opposing jungler qualify. A minimum of five such observations gates categorical
// labels in the renderer; performance thresholds retain Akari's five/eight-game
// rules. Flash slots describe recorded key habits independently from score and
// never establish an account operator's identity or certify paid boosting.

// EarlyGank retains verified early enemy-jungler involvement for one viewed player.
type EarlyGank struct {
	Deaths []int64 `json:"deaths"` // Qualifying death timestamps in milliseconds.
}

// PerformanceScore exposes complete eight-component averages and their shared sample count.
type PerformanceScore struct {
	Games int        `json:"games"` // Fully observed eligible score samples.
	Total float64    `json:"total"` // Eight components summed; maximum fifteen.
	Parts [8]float64 `json:"parts"` // KDA, wins, damage, taken, farm, gold, participation, vision.
	Label string     `json:"label"` // Threshold label; empty below minimum.
}

// PlayerSignals groups independent observations without conflating their sample coverage.
type PlayerSignals struct {
	Score      *PerformanceScore `json:"score"`      // Nil when full metrics unavailable.
	FlashD     int               `json:"flashD"`     // Recorded eligible flash-first game count.
	FlashF     int               `json:"flashF"`     // Recorded eligible flash-second game count.
	GankGames  int               `json:"gankGames"`  // Verified eligible timeline sample count.
	GankDeaths int               `json:"gankDeaths"` // Enemy-jungler involved early death total.
}

// IsJungler recognizes an explicitly assigned jungle role or recorded smite spell.
func (p Player) IsJungler() bool { return p.Position == "JUNGLE" || p.Spell1 == 11 || p.Spell2 == 11 }

// signalRange maps one observed value into a bounded linear component score.
func signalRange(value, low, high, weight float64) float64 {
	return math.Max(0, math.Min(1, (value-low)/(high-low))) * weight
}

// scoreMatch computes components only from a complete, fully measured match perspective.
// Callers apply competitive eligibility first. Missing farm, vision, damage, economy,
// or incoming damage rejects this score sample without affecting independent signals.
// Friendly totals provide per-game relative contribution baselines, so unusually
// long games do not outweigh shorter observations merely by producing larger raw
// totals. This helper leaves the first two components empty because aggregate
// KDA and win rate must be calculated over the final shared scoring sample.
func scoreMatch(m Match) ([8]float64, bool) {
	var parts [8]float64
	if !m.Complete() || m.MissingMetrics[0] || m.MissingMetrics[1] || m.MissingMetrics[2] {
		return parts, false
	}
	var own Player
	var damage, taken, gold, vision, kills int
	for _, p := range m.Players {
		if p.Local {
			own = p
		}
		if p.Team == m.Team {
			if p.Vision == nil {
				return parts, false
			}
			damage += p.Damage
			taken += p.Taken
			gold += p.Gold
			vision += *p.Vision
			kills += p.Kills
		}
	}
	if own.CS == nil || own.Vision == nil {
		return parts, false
	}
	// Five-player team averages define expected contribution, matching Akari's
	// normalized share baseline rather than comparing unrelated raw match totals.
	parts[2] = signalRange(float64(own.Damage)*5/float64(max(damage, 1)), 1, 2, 3)
	parts[3] = signalRange(float64(own.Taken)*5/float64(max(taken, 1)), 1, 2, 2)
	parts[4] = signalRange(float64(*own.CS)*60/float64(max(m.Duration, 1)), 5, 10, 2)
	parts[5] = signalRange(float64(own.Gold)*5/float64(max(gold, 1)), 1, 1.5, 2)
	parts[6] = signalRange(float64(own.Kills+own.Assists)/float64(max(kills, 1)), .3, 1, 2)
	parts[7] = signalRange(float64(*own.Vision)*5/float64(max(vision, 1)), 1, 2, 2)
	return parts, true
}

// SummarizeSignals prepares independent behavioral metrics for one competitive mode scope.
// Flash and gank samples retain their own denominators. Performance admits only
// complete metric samples, aggregates KDA and wins across those same samples,
// and averages the other six per-game transformed components before summing.
//
// This preserves the distinction between statistical absence and observed zero.
// A high score uses Akari's rule thresholds but does not consume rank or operator
// identity. The renderer therefore explains the label as recent performance,
// while early-gank labels require their independently measured timeline coverage.
func SummarizeSignals(matches []Match, mode Mode) PlayerSignals {
	var result PlayerSignals
	var scored PerformanceScore
	var kills, deaths, assists, wins int
	for _, m := range matches {
		if !Usable(m, mode) {
			continue
		}
		for _, p := range m.Players {
			if p.Local {
				if p.Spell1 == 4 {
					result.FlashD++
				}
				if p.Spell2 == 4 {
					result.FlashF++
				}
				break
			}
		}
		if m.EarlyGank != nil {
			result.GankGames++
			result.GankDeaths += len(m.EarlyGank.Deaths)
		}
		parts, ok := scoreMatch(m)
		if !ok {
			continue
		}
		scored.Games++
		kills += m.Kills
		deaths += m.Deaths
		assists += m.Assists
		if m.Win {
			wins++
		}
		for i := 2; i < 8; i++ {
			scored.Parts[i] += parts[i]
		}
	}
	if scored.Games > 0 {
		for i := 2; i < 8; i++ {
			scored.Parts[i] /= float64(scored.Games)
		}
		scored.Parts[0] = math.Min(1, math.Sqrt(float64(kills+assists)/float64(max(deaths, 1)))*3/7)
		scored.Parts[1] = signalRange(float64(wins)/float64(scored.Games), .5, 1, 1)
		for _, v := range scored.Parts {
			scored.Total += v
		}
		if scored.Games >= 5 && scored.Total >= 6.5 {
			scored.Label = "优异"
		}
		if scored.Games >= 8 && scored.Total >= 8 {
			scored.Label = "通天代"
		}
		result.Score = &scored
	}
	return result
}
