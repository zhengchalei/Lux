package domain

import "sort"

// summary.go prepares mode-consistent combat statistics outside the rendering path.
// Every result strip, hero usage count, and average consumes the same eligibility
// predicate, so changing mode cannot leave a recent strip describing a different
// sample from its displayed win rate. Raw rows remain available for inspection
// even when their custom, remake, short, or PVE status excludes them from analysis.
//
// Contribution averages each complete game's team fraction independently instead
// of dividing combined raw totals. Complete-roster coverage and metric availability
// remain separate: a complete game with missing damage can still supply observed
// gold, while an absent or zero denominator contributes no invented percentage.

// IsCustom identifies client-published custom queues excluded from competitive summaries.
func IsCustom(q int) bool {
	switch q {
	case 3100, 3110, 3120, 3130, 3200, 3210, 3220, 3230, 3270:
		return true
	}
	return false
}

// Usable applies shared competitive eligibility and an optional gameplay-mode filter.
// An empty mode includes every eligible queue; explicit mode filters never infer
// an unknown queue to be Summoner's Rift. Raw records remain inspectable separately.
func Usable(m Match, mode Mode) bool {
	if m.Duration < 180 || m.Remake || IsCustom(m.QueueID) || (m.GameType != "" && m.GameType != "MATCHED_GAME") {
		return false
	}
	switch m.QueueID {
	case 31, 32, 33, 34, 35, 36, 52, 90, 91, 92, 800, 801, 810, 820, 830, 831, 832, 840, 841, 842, 850, 851, 852, 860, 870, 880, 890, 950, 951, 960, 961, 981, 982, 990, 1030, 1031, 1032, 1040, 1041, 1050, 1051, 1060, 1061, 1070, 1071, 1800, 1810, 1820, 1830, 1840, 1850, 1860, 1870, 1880, 1890, 2000, 2010, 2020:
		return false
	}
	switch mode {
	case Ranked:
		switch m.QueueID {
		case 400, 420, 430, 440, 480, 490, 700, 720:
			return true
		}
		return false
	case Aram:
		return m.QueueID == 450
	case Mayhem:
		return m.QueueID == 2400
	}
	return true
}

// Complete reports whether one local perspective and both five-player teams are present.
// Ten arbitrary participants alone cannot establish five-versus-five contribution coverage.
// Arena groups or malformed team identifiers therefore remain incomplete for these comparisons.
func (m Match) Complete() bool {
	if len(m.Players) != 10 {
		return false
	}
	local, blue, red := 0, 0, 0
	for _, p := range m.Players {
		if p.Local {
			local++
		}
		switch p.Team {
		case 100:
			blue++
		case 200:
			red++
		}
	}
	return local == 1 && blue == 5 && red == 5
}

// Metric selects a raw combat measurement in the fixed contribution-axis order.
func (p Player) Metric(i int) int {
	switch i {
	case 0:
		return p.Damage
	case 1:
		return p.Gold
	case 2:
		return p.Taken
	case 3:
		return p.Heal
	case 4:
		return p.TowerDamage
	}
	return 0
}

// Contributions separates team share, radar normalization, tied rank, and normalized average.
// A complete roster and observed metric are required before making all-match
// claims. Zero team totals or maxima leave the metric unknown. The radar uses
// the match maximum independently from the friendly-team denominator, so a player
// owning thirty-two percent of team damage can still reach the outer edge when
// that raw damage is highest across the entire match. Equal values share rank.
func (m Match) Contributions(i int) (share, radar float64, rank int, average float64, ok bool) {
	if !m.Complete() || i < 0 || i >= 5 || m.MissingMetrics[i] {
		return
	}
	var own Player
	for _, p := range m.Players {
		if p.Local {
			own = p
			break
		}
	}
	total, maximum, all := 0, 0, 0
	rank = 1
	for _, p := range m.Players {
		v := p.Metric(i)
		all += v
		if p.Team == own.Team {
			total += v
		}
		if v > maximum {
			maximum = v
		}
		if v > own.Metric(i) {
			rank++
		}
	}
	if total == 0 || maximum == 0 {
		return 0, 0, 0, 0, false
	}
	return float64(own.Metric(i)) / float64(total), float64(own.Metric(i)) / float64(maximum), rank, float64(all) / float64(len(m.Players)*maximum), true
}

// PrepareProfile replaces every prepared mode summary alongside its coverage counters.
// Callers own the mutable profile exclusively during preparation, then publish it
// as an immutable observation. Existing source and scan context remains unchanged.
func PrepareProfile(p *Profile) {
	p.Summaries = make(map[string]Summary, 4)
	for _, mode := range []Mode{"", Ranked, Aram, Mayhem} {
		key := string(mode)
		if key == "" {
			key = "all"
		}
		p.Summaries[key] = Summarize(p.Matches, mode)
	}
	p.Summary = p.Summaries["all"]
	p.Coverage.Fetched = len(p.Matches)
	p.Coverage.Valid = p.Summary.Games
	p.Coverage.Complete = p.Summary.Contribution.Games
}

// Summarize aggregates eligible matches using distinct combat-average and KDA denominators.
// Recent results and hero counts consume the exact same selected sample. Empty
// samples retain zero games, which consumers must render as unavailable rather
// than observed zero-percent performance. Each contribution axis averages only
// per-match fractions with a valid denominator, preserving independent sample
// coverage and preventing high-total matches from silently dominating the result.
func Summarize(matches []Match, mode Mode) Summary {
	s := Summary{Recent: []Match{}, Heroes: []HeroSummary{}}
	heroes := map[int]*HeroSummary{}
	var kills, deaths, assists int
	shares := [3][]float64{}
	for _, m := range matches {
		if !Usable(m, mode) {
			continue
		}
		s.Games++
		if m.Win {
			s.Wins++
		}
		kills += m.Kills
		deaths += m.Deaths
		assists += m.Assists
		if len(s.Recent) < 10 {
			s.Recent = append(s.Recent, m)
		}
		h := heroes[m.ChampionID]
		if h == nil {
			h = &HeroSummary{ChampionID: m.ChampionID}
			heroes[m.ChampionID] = h
		}
		h.Games++
		if m.Win {
			h.Wins++
		}
		h.Kills += float64(m.Kills)
		h.Deaths += float64(m.Deaths)
		h.Assists += float64(m.Assists)
		if m.Complete() {
			s.Contribution.Games++
		}
		for j, metric := range []int{0, 2, 1} {
			if share, _, _, _, ok := m.Contributions(metric); ok {
				shares[j] = append(shares[j], share)
			}
		}
	}
	if s.Games > 0 {
		s.WinRate = float64(s.Wins) * 100 / float64(s.Games)
		s.Kills = float64(kills) / float64(s.Games)
		s.Deaths = float64(deaths) / float64(s.Games)
		s.Assists = float64(assists) / float64(s.Games)
		s.KDA = float64(kills+assists) / float64(max(deaths, 1))
	}
	for _, h := range heroes {
		h.WinRate = float64(h.Wins) * 100 / float64(h.Games)
		h.KDA = (h.Kills + h.Assists) / max(h.Deaths, 1)
		h.Kills /= float64(h.Games)
		h.Deaths /= float64(h.Games)
		h.Assists /= float64(h.Games)
		s.Heroes = append(s.Heroes, *h)
	}
	sort.Slice(s.Heroes, func(i, j int) bool {
		if s.Heroes[i].Games == s.Heroes[j].Games {
			return s.Heroes[i].ChampionID < s.Heroes[j].ChampionID
		}
		return s.Heroes[i].Games > s.Heroes[j].Games
	})
	s.Contribution.DamageGames = len(shares[0])
	s.Contribution.TakenGames = len(shares[1])
	s.Contribution.GoldGames = len(shares[2])
	s.Contribution.DamageShare = mean(shares[0])
	s.Contribution.TakenShare = mean(shares[1])
	s.Contribution.GoldShare = mean(shares[2])
	return s
}

// mean returns an observed fraction average, retaining nil for absent denominators.
func mean(values []float64) *float64 {
	if len(values) == 0 {
		return nil
	}
	sum := 0.
	for _, v := range values {
		sum += v
	}
	sum /= float64(len(values))
	return &sum
}
