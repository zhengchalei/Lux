package updater

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/netproxy"
)

// service.go owns automatic release checks, one cancellable download, and pending
// installation admission. The default enables startup and six-hour checks, while
// disabling updates persists immediately and revokes publication from work that
// was already running. Network failures retain the current executable and expose
// their cause through a small settings DTO instead of blocking application startup.
//
// The installed executable is never changed by a running download. A validated
// candidate belongs to one temporary directory until explicit application exit
// arms the native worker. Installation and configuration serialize with checks,
// so a completed download cannot replace a pending installer or survive a stop
// through stale state publication. Browser previews create an inactive service.

// Status exposes update progress without local paths, process handles, or player observations.
type Status struct {
	Supported bool   `json:"supported"` // Real desktop update availability.
	Enabled   bool   `json:"enabled"`   // Persisted automatic update preference.
	Current   string `json:"current"`   // Installed application semantic version.
	Latest    string `json:"latest"`    // Latest observed release version.
	State     string `json:"state"`     // Current check or installation stage.
	Progress  int    `json:"progress"`  // Whole asset download percentage.
	Message   string `json:"message"`   // Current readable operation result.
	CheckedAt string `json:"checkedAt"` // Latest GitHub observation time in RFC3339.
}

// candidate retains a verified download for an installation after this process exits.
type candidate struct {
	Directory string // Private update session directory.
	Path      string // Complete verified candidate file.
	Digest    string // GitHub-provided SHA-256 digest string.
	Version   string // Candidate stable semantic version.
}

// Service serializes update preference, cancellation, and native installation ownership.
type Service struct {
	// mu protects status, busy, cancel, pending, armed, closed, and recheck as one
	// admission state. Construction fixes root, directory, target, and the transport
	// before scheduled checks or desktop IPC can access this service concurrently.
	mu        sync.Mutex         // Serializes settings and job publication.
	status    Status             // Small user-facing update observation.
	root      context.Context    // Application cancellation boundary.
	directory string             // Per-user updater preference directory.
	target    string             // Installed desktop executable path.
	http      *http.Client       // Proxy-aware public GitHub transport.
	busy      bool               // One admitted check or download.
	cancel    context.CancelFunc // Revokes current network operation.
	pending   *candidate         // Verified candidate awaiting application exit.
	armed     bool               // Native replacement worker already admitted.
	closed    bool               // No new operations after shutdown begins.
	recheck   bool               // Re-enable waits for cancelled work to finish.
}

// New constructs an inactive updater with a persisted, default-enabled preference.
// Desktop callers pass their executable path; browser previews pass an empty
// target and cannot start downloads or installation. Start owns scheduled work.
func New(ctx context.Context, current, directory, target string) *Service {
	s := &Service{root: ctx, directory: directory, target: target, status: Status{Supported: target != "", Enabled: true, Current: current, State: "idle", Message: "等待检查更新"}}
	var preference struct {
		Enabled bool `json:"enabled"`
	}
	preference.Enabled = true
	if raw, err := os.ReadFile(filepath.Join(directory, "update.json")); err == nil && json.Unmarshal(raw, &preference) == nil {
		s.status.Enabled = preference.Enabled
	}
	if !s.status.Supported {
		s.status.Message = "自动更新仅在桌面发行版中运行"
	}
	if !s.status.Enabled {
		s.status.Message = "自动更新已关闭"
	}
	if s.status.Supported {
		cleanupSessions(directory)
	}
	s.http = &http.Client{Transport: netproxy.Transport(), CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 || !trustedURL(req.URL.String()) {
			return errors.New("更新重定向地址不受支持")
		}
		return nil
	}}
	// A prior worker records installation failures independently of the process
	// being replaced, allowing the next ordinary launch to explain the outcome.
	if raw, err := os.ReadFile(filepath.Join(directory, "update-result.json")); s.status.Supported && err == nil {
		var result struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(raw, &result) == nil && result.Error != "" {
			s.status.State, s.status.Message = "error", "上次更新未完成："+result.Error
		}
		os.Remove(filepath.Join(directory, "update-result.json"))
	}
	return s
}

// Start schedules bounded checks until the application context is cancelled.
// Call once per desktop lifetime. No renderer or application window is needed,
// and disabled services remain idle until a later preference enables a check.
func (s *Service) Start() {
	go func() {
		s.Check()
		// Scheduled checks avoid repeatedly consuming GitHub's anonymous API quota.
		timer := time.NewTicker(6 * time.Hour)
		defer timer.Stop()
		for {
			select {
			case <-s.root.Done():
				return
			case <-timer.C:
				s.Check()
			}
		}
	}()
}

// Status returns a coherent scalar observation without copying historical game data.
func (s *Service) Status() Status { s.mu.Lock(); defer s.mu.Unlock(); return s.status }

// SetEnabled persists update preference before cancelling or enabling background work.
// A failed write leaves current behavior unchanged. Disabling cancels an ongoing
// request and discards its candidate, while an admitted native installer cannot
// be revoked after application exit has started. Callers receive that conflict
// explicitly rather than seeing a setting change that cannot still take effect.
func (s *Service) SetEnabled(enabled bool) error {
	s.mu.Lock()
	if s.armed || s.closed {
		s.mu.Unlock()
		return errors.New("正在退出或安装更新")
	}
	if err := os.MkdirAll(s.directory, 0700); err != nil {
		s.mu.Unlock()
		return err
	}
	raw, _ := json.Marshal(struct {
		Enabled bool `json:"enabled"`
	}{enabled})
	if err := os.WriteFile(filepath.Join(s.directory, "update.json"), raw, 0600); err != nil {
		s.mu.Unlock()
		return err
	}
	if enabled && !s.status.Enabled && s.busy {
		s.recheck = true
	}
	s.status.Enabled = enabled
	if !enabled {
		if s.cancel != nil {
			s.cancel()
		}
		s.discardLocked()
		s.status.State, s.status.Message, s.status.Progress = "idle", "自动更新已关闭", 0
	}
	s.mu.Unlock()
	if enabled {
		s.Check()
	}
	return nil
}

// Check admits one asynchronous stable-release check and automatically downloads newer content.
// Concurrent requests coalesce. The settings toggle and service context own
// cancellation; this method returns immediately so desktop IPC never waits on GitHub.
func (s *Service) Check() {
	s.mu.Lock()
	if !s.status.Supported || !s.status.Enabled || s.busy || s.closed || s.armed {
		s.mu.Unlock()
		return
	}
	// Downloads receive a separate bounded lifetime from metadata checks, allowing
	// slower connections without holding a desktop bridge call open during transfer.
	ctx, cancel := context.WithTimeout(s.root, 10*time.Minute)
	s.busy, s.cancel = true, cancel
	s.status.State, s.status.Message = "checking", "正在检查 GitHub Release…"
	s.mu.Unlock()
	go func() { defer cancel(); s.check(ctx) }()
}

// check owns one network attempt and publishes its candidate only while still authorized.
// Call only after Check admits the operation. Every failure cleans its temporary
// session and preserves the installed executable. A previous ready candidate is
// retained during checks, then replaced only by another fully validated version.
// Cancellation wins publication under mu, including a successful reply arriving
// after updates were disabled. Progress uses the same gate as final publication.
func (s *Service) check(ctx context.Context) {
	checkCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
	latestVersion, item, err := latest(checkCtx, s.http, s.status.Current)
	cancel()
	var ready *candidate
	if err == nil && item != nil {
		s.mu.Lock()
		already := s.pending != nil && s.pending.Version == latestVersion && s.pending.Digest == item.Digest
		if ctx.Err() == nil && s.status.Enabled {
			s.status.Latest = latestVersion
			if !already {
				s.status.State, s.status.Message, s.status.Progress = "downloading", "正在下载新版 Lux.exe…", 0
			}
		}
		s.mu.Unlock()
		if !already && ctx.Err() == nil {
			err = os.MkdirAll(s.directory, 0700)
			var directory string
			if err == nil {
				directory, err = os.MkdirTemp(s.directory, "session-")
			}
			if err == nil {
				ready = &candidate{Directory: directory, Path: filepath.Join(directory, "pending.exe"), Digest: item.Digest, Version: latestVersion}
				err = download(ctx, s.http, *item, ready.Path, func(value int) {
					s.mu.Lock()
					if ctx.Err() == nil && s.status.Enabled {
						s.status.Progress = value
					}
					s.mu.Unlock()
				})
			}
		}
	}
	s.mu.Lock()
	// Re-enable can arrive before a cancelled request returns. Release this
	// job's admission before starting the fresh check so that demand is preserved.
	defer func() {
		recheck := s.recheck && s.status.Enabled && !s.closed
		s.recheck = false
		s.mu.Unlock()
		if recheck {
			s.Check()
		}
	}()
	s.busy, s.cancel = false, nil
	if ctx.Err() != nil || !s.status.Enabled || s.closed || s.armed {
		if ready != nil {
			removeCandidate(ready)
		}
		if s.status.Enabled && !s.closed && !s.armed {
			s.status.State, s.status.Message = "error", "更新请求超时或已取消，可稍后重试"
		}
		return
	}
	s.status.CheckedAt = time.Now().Format(time.RFC3339)
	if err != nil {
		if ready != nil {
			removeCandidate(ready)
		}
		s.status.State, s.status.Message = "error", err.Error()
		return
	}
	s.status.Latest = latestVersion
	if ready != nil {
		s.discardLocked()
		s.pending = ready
	}
	if s.pending != nil {
		s.status.State, s.status.Message, s.status.Progress = "ready", "新版已下载，将在退出 Lux 后自动安装", 100
	} else {
		s.status.State, s.status.Message, s.status.Progress = "current", "已是最新版本", 0
	}
}

// PrepareExit arms a verified replacement worker before the desktop process quits.
// Call before cancelling the application context. Restart explicitly requests a
// fresh main window after installation; ordinary exit installs without reopening.
// A pending update is admitted at most once. Its worker handshake verifies
// writable target placement before this method returns,
// allowing callers to preserve their current application when preparation fails.
func (s *Service) PrepareExit(restart bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.armed {
		return nil
	}
	if s.pending == nil || !s.status.Enabled {
		if restart {
			return errors.New("没有已下载的更新")
		}
		return nil
	}
	if err := launchWorker(s.target, *s.pending, restart, filepath.Join(s.directory, "update-result.json")); err != nil {
		s.status.State, s.status.Message = "error", "更新暂未安装："+err.Error()
		result, _ := json.Marshal(struct {
			Error string `json:"error"`
		}{err.Error()})
		_ = os.WriteFile(filepath.Join(s.directory, "update-result.json"), result, 0600)
		return err
	}
	s.armed = true
	s.status.State, s.status.Message = "installing", "退出后安装更新…"
	return nil
}

// Close revokes network publication and cleans candidates not transferred to a worker.
// Call once application exit is committed. An armed worker owns its candidate
// until replacement finishes, so cancellation cannot delete files beneath installation.
func (s *Service) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	if s.cancel != nil {
		s.cancel()
	}
	if !s.armed {
		s.discardLocked()
	}
}

// discardLocked releases the current candidate while its owning service mutex is held.
func (s *Service) discardLocked() {
	if s.pending != nil {
		removeCandidate(s.pending)
		s.pending = nil
	}
}

// removeCandidate removes only the two known files owned by one download session.
func removeCandidate(p *candidate) { os.Remove(p.Path); os.Remove(p.Directory) }

// cleanupSessions removes known artifacts from stale, inactive update sessions only.
// Recent sessions remain untouched because a copied worker can still be finishing
// replacement during relaunch. Unknown files are never recursively removed.
func cleanupSessions(directory string) {
	entries, _ := os.ReadDir(directory)
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "session-") {
			continue
		}
		info, err := entry.Info()
		if err != nil || time.Since(info.ModTime()) < time.Hour {
			continue
		}
		path := filepath.Join(directory, entry.Name())
		for _, name := range []string{"pending.exe", "worker.exe", "plan.json"} {
			os.Remove(filepath.Join(path, name))
		}
		os.Remove(path)
	}
}
