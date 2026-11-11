package client

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"lux/internal/domain"
	"lux/internal/history"
)

// application.go persists desktop preferences and accepts explicitly enabled match
// invitations independently from champion automation. A cycle is associated with
// both the account generation and observed ReadyCheck entry, so a background
// window closing cannot enable champion selection or repeat a consumed invitation.
// Only the client synchronization worker advances the observed phase cycle.
//
// The acceptance worker delays outside the state lock and rechecks the client
// phase, account, and response before admission. Settings replacement cancels an
// admitted transport, while its attempt stamp survives toggle changes within
// the same cycle. Uncertain write failures remain manual actions instead of
// being retried; already processed server actions cannot be recalled.

// loadApplicationSettings restores independent preferences with bounded acceptance delay values.
// A missing or damaged file keeps tray closing enabled and acceptance disabled.
// Invalid saved delays select two seconds rather than delaying indefinitely.
func loadApplicationSettings() domain.ApplicationSettings {
	settings := domain.ApplicationSettings{CloseToTray: true, AcceptDelaySeconds: 2}
	if raw, err := os.ReadFile(filepath.Join(dataDirectory(), "application.json")); err == nil {
		candidate := settings
		if decodeCompatible(raw, &candidate) == nil {
			settings = candidate
		}
	}
	if settings.AcceptDelaySeconds < 0 || settings.AcceptDelaySeconds > 10 {
		settings.AcceptDelaySeconds = 2
	}
	return settings
}

// ApplicationSettings returns current preferences without copying player or catalog observations.
func (r *Runtime) ApplicationSettings() domain.ApplicationSettings {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.state.Application
}

// SaveApplicationSettings applies desktop preferences and revokes preceding acceptance permission.
// Delay values outside zero through ten seconds are rejected before mutation.
// The live switch still applies when persistence fails, allowing immediate stops.
// Preference writers serialize mutation with their corresponding file replacement,
// so a later acknowledged control cannot be overwritten by an earlier save.
// Snapshot listeners run after unlocking and therefore observe the same preference
// generation that the worker checks before admitting a client command.
func (r *Runtime) SaveApplicationSettings(settings domain.ApplicationSettings) error {
	if settings.AcceptDelaySeconds < 0 || settings.AcceptDelaySeconds > 10 {
		return fmt.Errorf("接受延迟须为 0–10 秒")
	}
	r.preferencesMu.Lock()
	defer r.preferencesMu.Unlock()
	r.changed(func() {
		// Revoke a scheduled or admitted request before publishing replacement settings, including switches disabled while transport is pending.
		r.acceptEpoch++
		if r.acceptCancel != nil {
			r.acceptCancel()
			r.acceptCancel = nil
		}
		r.state.Application = settings
		r.state.AcceptStatus = acceptIdleStatus(settings.AutoAccept)
	})
	return savePreference("application.json", settings)
}

// acceptIdleStatus describes whether the independent match invitation worker is enabled.
func acceptIdleStatus(enabled bool) string {
	if enabled {
		return "等待匹配成功"
	}
	return "自动接受已关闭"
}

// readyCheck carries client response observations without retaining matchmaking participant identities.
type readyCheck struct {
	State          string `json:"state"`          // Observed invitation lifecycle state.
	PlayerResponse string `json:"playerResponse"` // Current player's invitation response.
}

// pending reports whether this player can still accept an active invitation.
func (c readyCheck) pending() bool { return c.State == "InProgress" && c.PlayerResponse == "None" }

// acceptLoop owns serial cycle detection, delay scheduling, and transport admission.
// The root context ends its polling with the runtime. Disabled or disconnected
// sessions perform no ready-check requests, and repeated observations preserve
// the first deadline rather than postponing it with every snapshot refresh.
// A settings replacement changes the scheduling stamp and starts its newly chosen
// delay, while an already consumed cycle remains unavailable. Fresh client reads
// run outside the snapshot lock so background history remains independently usable.
func (r *Runtime) acceptLoop() {
	var scheduled string
	var due time.Time
	for r.root.Err() == nil {
		r.mu.Lock()
		settings, epoch := r.state.Application, r.acceptEpoch
		key := fmt.Sprintf("%d:%d", r.epoch, r.readyCycle)
		active := settings.AutoAccept && r.state.Connected && r.state.Phase == "ReadyCheck" && r.access != nil && r.session != nil && r.session.Err() == nil
		attempted := r.acceptAttempt == key
		r.mu.Unlock()
		if !active || attempted {
			scheduled = ""
		} else {
			stamp := fmt.Sprintf("%s:%d", key, epoch)
			if stamp != scheduled {
				scheduled, due = stamp, time.Now().Add(time.Duration(settings.AcceptDelaySeconds)*time.Second)
				r.acceptStatus(key, epoch, fmt.Sprintf("匹配成功 · %d 秒后接受", settings.AcceptDelaySeconds))
			}
			if !time.Now().Before(due) {
				r.acceptReady(key, epoch)
				scheduled = ""
			}
		}
		if !wait(r.root, 400*time.Millisecond) {
			return
		}
	}
}

// acceptStatus publishes worker feedback only while its account and permission remain current.
// It avoids duplicate revisions so an idle background application sends no UI
// events merely because the acceptance worker checked its current permission.
func (r *Runtime) acceptStatus(key string, epoch uint64, status string) {
	r.mu.Lock()
	if r.acceptEpoch != epoch || fmt.Sprintf("%d:%d", r.epoch, r.readyCycle) != key || r.state.AcceptStatus == status {
		r.mu.Unlock()
		return
	}
	r.state.AcceptStatus = status
	r.state.Revision++
	listener := r.listener
	r.mu.Unlock()
	if listener != nil {
		listener()
	}
}

// acceptReady revalidates one invitation and consumes its attempt before submitting acceptance.
// Callers use an observed account-cycle key and settings generation. Fresh phase,
// player response, and identity reads prevent a delayed task accepting after an
// already observed decline or account replacement. Session cancellation and the
// final locked admission protect concurrent switch changes before transport.
// A failed POST still consumes this cycle because its server outcome may be
// unknown; later polling cannot repeat an ambiguous mutating operation.
func (r *Runtime) acceptReady(key string, epoch uint64) {
	r.mu.Lock()
	if !r.acceptAllowedLocked(key, epoch) {
		r.mu.Unlock()
		return
	}
	access, ctx, account := *r.access, r.session, r.state.Account.PUUID
	r.mu.Unlock()
	var phase string
	var check readyCheck
	if get(ctx, access, "/lol-gameflow/v1/gameflow-phase", &phase) != nil || phase != "ReadyCheck" {
		r.wakeSync()
		return
	}
	if get(ctx, access, "/lol-matchmaking/v1/ready-check", &check) != nil {
		return
	}
	if !check.pending() {
		if check.PlayerResponse == "Accepted" || check.PlayerResponse == "Declined" {
			r.mu.Lock()
			if r.acceptAllowedLocked(key, epoch) {
				r.acceptAttempt = key
			}
			r.mu.Unlock()
			r.acceptStatus(key, epoch, map[bool]string{true: "已接受对局", false: "已手动拒绝"}[check.PlayerResponse == "Accepted"])
		}
		return
	}
	person, err := resolve(ctx, access, "", "")
	if err != nil || domain.Identity(person["puuid"]) != account {
		r.wakeSync()
		return
	}
	r.mu.Lock()
	if !r.acceptAllowedLocked(key, epoch) {
		r.mu.Unlock()
		return
	}
	// Consume before network submission: cancellation or a missing response cannot make a second POST safe for this same invitation.
	r.acceptAttempt = key
	writeCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	r.acceptCancel = cancel
	r.mu.Unlock()
	err = submitAccept(writeCtx, access)
	cancel()
	if err != nil {
		r.acceptStatus(key, epoch, "接受结果未确认，请在客户端检查")
	} else {
		r.acceptStatus(key, epoch, "已自动接受对局")
	}
	r.wakeSync()
}

// acceptAllowedLocked checks the current account, phase cycle, and independent switch permission.
// The caller must hold mu across this check and any admission mutation. Session
// cancellation is checked again by transport if ownership changes after unlocking.
func (r *Runtime) acceptAllowedLocked(key string, epoch uint64) bool {
	return !r.closed && r.state.Application.AutoAccept && r.state.Connected && r.state.Account != nil && r.access != nil && r.session != nil && r.session.Err() == nil && r.acceptEpoch == epoch && r.state.Phase == "ReadyCheck" && fmt.Sprintf("%d:%d", r.epoch, r.readyCycle) == key && r.acceptAttempt != key
}

// submitAccept sends one invitation acceptance without retrying an uncertain server outcome.
func submitAccept(ctx context.Context, access history.Access) error {
	return request(ctx, access, "POST", "/lol-matchmaking/v1/ready-check/accept", nil, nil)
}
