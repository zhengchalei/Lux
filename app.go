package main

import (
	"context"
	"fmt"

	"lux/internal/client"
	"lux/internal/domain"
	"lux/internal/publicdata"
	"lux/internal/updater"
)

// app.go exposes the typed desktop boundary between Vue and Lux's Go services.
// Methods delegate client data work to its asynchronous runtime and public work
// to its independent provider service. Client credentials remain private to Go;
// frontend calls exchange normalized observations and explicit user commands.
// Software updates use an independent service and never require player snapshots.

// App binds the supported product actions without exposing transport implementation details.
type App struct {
	runtime *client.Runtime     // Independent client and automation lifecycle.
	public  *publicdata.Service // Public source refresh and matchup service.
	panel   *draftPanel         // Independent compact client companion window.
	desktop *desktopController  // Tray and main-window resource lifetime.
	updates *updater.Service    // Independent GitHub update service.
}

// SaveApplicationSettings applies independent background and match acceptance preferences persistently.
func (a *App) SaveApplicationSettings(settings domain.ApplicationSettings) error {
	return a.runtime.SaveApplicationSettings(settings)
}

// GetDesktopStatus reports whether native tray recovery is available in this execution.
func (a *App) GetDesktopStatus() DesktopStatus {
	if a.desktop == nil {
		return DesktopStatus{}
	}
	return a.desktop.Status()
}

// GetDraftSnapshot returns selection data without copying historical player records.
func (a *App) GetDraftSnapshot() domain.DraftPanelSnapshot { return a.runtime.DraftSnapshot() }

// GetDraftSkins reads client-permitted appearances for the displayed current hero.
func (a *App) GetDraftSkins(ctx context.Context, key string, champion int) (domain.DraftSkins, error) {
	return a.runtime.GetDraftSkins(ctx, key, champion)
}

// SelectDraftChampion explicitly swaps one available replacement-bench champion after validation.
func (a *App) SelectDraftChampion(ctx context.Context, key string, current, target int) error {
	return a.runtime.SelectDraftChampion(ctx, key, current, target)
}

// SetDraftSkin explicitly applies one usable skin after fresh client validation.
func (a *App) SetDraftSkin(ctx context.Context, key string, champion, skin int) error {
	return a.runtime.SetDraftSkin(ctx, key, champion, skin)
}

// GetSkinPreview returns an authenticated thumbnail as a bounded image data URL.
func (a *App) GetSkinPreview(ctx context.Context, key string, champion, skin int) (string, error) {
	return a.runtime.GetSkinPreview(ctx, key, champion, skin)
}

// GetDraftPanelStatus reports companion preferences or browser-preview window unavailability.
func (a *App) GetDraftPanelStatus() DraftPanelStatus {
	if a.panel == nil {
		return DraftPanelStatus{}
	}
	return a.panel.Status()
}

// SetDraftPanel opens, hides, or detaches the independent client companion window.
// Browser preview has no native companion and returns a descriptive error. The
// desktop controller applies preferences asynchronously without activating the client.
func (a *App) SetDraftPanel(enabled, pinned bool) error {
	if a.panel == nil {
		return fmt.Errorf("贴边窗口请在桌面版中使用")
	}
	a.panel.Configure(enabled, pinned)
	return nil
}

// SetDraftPanelCollapsed folds or expands the native client-attached selection companion.
func (a *App) SetDraftPanelCollapsed(collapsed bool) error {
	if a.panel == nil {
		return fmt.Errorf("贴边窗口请在桌面版中使用")
	}
	a.panel.Collapse(collapsed)
	return nil
}

// GetSnapshot returns a coherent revision of browser-visible application observations.
func (a *App) GetSnapshot() domain.Snapshot { return a.runtime.Snapshot() }

// GetRevision reads the publication stamp without copying any player histories.
// Recovery checks use this scalar before requesting a full snapshot, preventing
// unchanged large analysis samples from crossing the bridge every few seconds.
func (a *App) GetRevision() uint64 { return a.runtime.CurrentRevision() }

// Lookup starts an identity lookup using an optional exact queue filter.
func (a *App) Lookup(name string, queueID int) error { return a.runtime.Lookup(name, queueID) }

// SelectSummoner confirms one current search candidate before reading its match history.
func (a *App) SelectSummoner(searchID uint64, puuid string, queueID int) error {
	return a.runtime.SelectSummoner(searchID, puuid, queueID)
}

// GetSummonerIcon returns a bounded client asset observed in the candidate list.
func (a *App) GetSummonerIcon(ctx context.Context, icon int) (string, error) {
	return a.runtime.GetSummonerIcon(ctx, icon)
}

// GetTimeline reads evidence-backed phases for the selected recorded match and opponent.
func (a *App) GetTimeline(ctx context.Context, puuid string, id uint64, opponent int) (domain.TimelineReview, error) {
	return a.runtime.GetTimeline(ctx, puuid, id, opponent)
}

// GetAISettings returns provider metadata and saved-key presence without revealing credentials.
func (a *App) GetAISettings() domain.AISettings { return a.runtime.GetAISettings() }

// SaveAISettings persists compatible model preferences with a Windows-protected credential.
func (a *App) SaveAISettings(input domain.AISettingsInput) (domain.AISettings, error) {
	return a.runtime.SaveAISettings(input)
}

// AnalyzeTimeline explicitly requests model coaching for one observed opposing comparison.
func (a *App) AnalyzeTimeline(ctx context.Context, puuid string, id uint64, opponent int, requestID string) (domain.AIReview, error) {
	return a.runtime.AnalyzeTimeline(ctx, puuid, id, opponent, requestID)
}

// CancelAIAnalysis stops the active model request without discarding observed timeline facts.
func (a *App) CancelAIAnalysis(requestID string) { a.runtime.CancelAIAnalysis(requestID) }

// QuerySelf starts history lookup for the currently observed local account.
func (a *App) QuerySelf(queueID int) error { return a.runtime.QuerySelf(queueID) }

// ReviewLastGame loads the latest readable competitive match as labeled history.
func (a *App) ReviewLastGame() error { return a.runtime.ReviewLastGame() }

// RefreshBattle refreshes current public players while retaining prior successful observations.
func (a *App) RefreshBattle() error { return a.runtime.RefreshBattle() }

// RetryScout retries one publicly identified player in the currently observed roster.
func (a *App) RetryScout(puuid string) error { return a.runtime.RetryScout(puuid) }

// SetAnalysis applies bounded history depth and the current-mode analysis preference.
func (a *App) SetAnalysis(count int, sameMode bool) error {
	return a.runtime.SetAnalysis(count, sameMode)
}

// SaveAutomation applies user rules and an explicit permission to execute them.
// Every application launch starts disabled. Enabling from Vue is an intentional
// setting action; ordinary data reads and renderer interactions cannot enable it.
func (a *App) SaveAutomation(settings domain.AutomationSettings, enabled bool) error {
	return a.runtime.SaveAutomation(settings, enabled)
}

// StopAutomation immediately prevents later automatic draft submissions from this runtime.
func (a *App) StopAutomation() { a.runtime.StopAutomation() }

// GetMatchDetail retrieves complete combat observations for the explicitly identified perspective.
// The calling page's context bounds slow work. Unknown player identity remains an
// error, so another participant's match cannot replace the selected player's details.
func (a *App) GetMatchDetail(ctx context.Context, puuid string, id uint64) (domain.Match, error) {
	return a.runtime.Detail(ctx, puuid, id)
}

// GetParticipantProfile reads the clicked public participant of an already displayed match.
// The runtime validates its recorded identity and retains the bounded back trail;
// the returned profile leaves the workspace's main history query unchanged.
func (a *App) GetParticipantProfile(ctx context.Context, key, owner string, id uint64, target string, retained []string) (domain.Profile, error) {
	return a.runtime.GetParticipantProfile(ctx, key, owner, id, target, retained)
}

// CloseParticipantExplorer releases one dismissed player page's private profile observations.
// Only the matching explorer key is released, so a delayed dismissal cannot
// cancel a later page. Pending reads stop independently of the main workspace.
func (a *App) CloseParticipantExplorer(key string) { a.runtime.CloseParticipantExplorer(key) }

// GetReplay checks client replay availability for one currently visible historical match.
func (a *App) GetReplay(ctx context.Context, puuid string, id uint64) (domain.Replay, error) {
	return a.runtime.GetReplay(ctx, puuid, id)
}

// ReplayAction explicitly downloads or watches a freshly validated historical match replay.
func (a *App) ReplayAction(ctx context.Context, puuid string, id uint64, action string) error {
	return a.runtime.ReplayAction(ctx, puuid, id, action)
}

// GetMatchups fetches final match outcome observations against one public opposing champion.
func (a *App) GetMatchups(ctx context.Context, enemy int, position string) ([]domain.Matchup, error) {
	return a.public.Matchups(ctx, enemy, position)
}

// InspectChampion retrieves version-matched Mayhem adjustments for the selected public champion.
func (a *App) InspectChampion(ctx context.Context, id int) error { return a.public.Inspect(ctx, id) }
