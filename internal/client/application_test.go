package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"lux/internal/domain"
	"lux/internal/history"
)

// application_test.go checks independent ReadyCheck commands against isolated TLS
// responses. These fixtures never discover a running client, and write counts
// protect cancellation, identity ownership, and uncertain-outcome consumption.

// acceptFixture configures observed responses and counts actual acceptance submissions.
type acceptFixture struct {
	runtime        *Runtime     // Account-bound runtime under test.
	phase          string       // Fixture gameflow phase response.
	response       string       // Fixture local invitation response.
	account        string       // Fixture current account identity.
	fail           bool         // Fail after receiving a POST.
	beforeIdentity func()       // Optional pre-admission settings revocation.
	posts          atomic.Int32 // Actually received acceptance submissions.
}

// newAcceptFixture provides an authenticated runtime without starting real client discovery.
// Tests execute sequentially because the loopback transport is temporarily replaced.
// Cleanup cancels owned workers before restoring that transport and closing TLS.
func newAcceptFixture(t *testing.T) *acceptFixture {
	t.Helper()
	t.Setenv("LOCALAPPDATA", t.TempDir())
	f := &acceptFixture{phase: "ReadyCheck", response: "None", account: "fixture-local"}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		var result any
		switch request.URL.Path {
		case "/lol-gameflow/v1/gameflow-phase":
			result = f.phase
		case "/lol-matchmaking/v1/ready-check":
			result = readyCheck{State: "InProgress", PlayerResponse: f.response}
		case "/lol-summoner/v1/current-summoner":
			if f.beforeIdentity != nil {
				f.beforeIdentity()
			}
			result = map[string]any{"puuid": f.account}
		case "/lol-matchmaking/v1/ready-check/accept":
			if request.Method != "POST" {
				t.Error("acceptance is not POST")
			}
			f.posts.Add(1)
			if f.fail {
				w.WriteHeader(500)
			} else {
				w.WriteHeader(204)
			}
			return
		default:
			t.Errorf("unexpected endpoint %s", request.URL.Path)
			w.WriteHeader(404)
			return
		}
		_ = json.NewEncoder(w).Encode(result)
	}))
	address, _ := url.Parse(server.URL)
	port, _ := strconv.Atoi(address.Port())
	previous := localTransport
	localTransport = server.Client()
	r := New(domain.Database{}, history.New(domain.Database{}))
	r.root, r.cancel = context.WithCancel(context.Background())
	r.session, r.endSession = context.WithCancel(r.root)
	r.access = &history.Access{Port: port, Token: "fixture-only", Account: f.account}
	r.epoch, r.readyCycle = 1, 1
	r.state.Account = &domain.Account{PUUID: f.account}
	r.state.Connected, r.state.Phase = true, "ReadyCheck"
	r.state.Application = domain.ApplicationSettings{CloseToTray: true, AutoAccept: true}
	f.runtime = r
	t.Cleanup(func() { r.Close(); localTransport = previous; server.Close() })
	return f
}

// TestAcceptAdmission rejects responded, superseded, disabled, and different-account invitations.
// Only the valid pending invitation reaches the protocol POST endpoint; renderer
// visibility and champion automation permission do not participate in admission.
func TestAcceptAdmission(t *testing.T) {
	for _, name := range []string{"pending", "accepted", "declined", "phase", "account", "disabled", "revoke-before-submit"} {
		t.Run(name, func(t *testing.T) {
			f := newAcceptFixture(t)
			switch name {
			case "accepted":
				f.response = "Accepted"
			case "declined":
				f.response = "Declined"
			case "phase":
				f.phase = "ChampSelect"
			case "account":
				f.account = "another-account"
			case "disabled":
				f.runtime.state.Application.AutoAccept = false
			case "revoke-before-submit":
				f.beforeIdentity = func() { _ = f.runtime.SaveApplicationSettings(domain.ApplicationSettings{CloseToTray: true}) }
			}
			f.runtime.acceptReady("1:1", 0)
			want := int32(0)
			if name == "pending" {
				want = 1
			}
			if f.posts.Load() != want {
				t.Fatalf("received %d posts, want %d", f.posts.Load(), want)
			}
		})
	}
}

// TestAcceptConsumesAmbiguousWrites preserves one attempt across polling and switch replacement.
// A later observed phase cycle permits a fresh invitation, including when the
// preceding server returned an error after receiving the original acceptance.
func TestAcceptConsumesAmbiguousWrites(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(fmt.Sprint(failed), func(t *testing.T) {
			f := newAcceptFixture(t)
			f.fail = failed
			f.runtime.acceptReady("1:1", 0)
			f.runtime.acceptReady("1:1", 0)
			settings := f.runtime.ApplicationSettings()
			settings.AutoAccept = false
			_ = f.runtime.SaveApplicationSettings(settings)
			settings.AutoAccept = true
			_ = f.runtime.SaveApplicationSettings(settings)
			f.runtime.acceptReady("1:1", 2)
			if f.posts.Load() != 1 {
				t.Fatal("same invitation submitted more than once")
			}
			f.runtime.readyCycle++
			f.runtime.acceptReady("1:2", 2)
			if f.posts.Load() != 2 {
				t.Fatal("new observed cycle did not receive one attempt")
			}
		})
	}
}

// TestAcceptDelayCancellation revokes permission after a real worker schedules its deadline.
// Keeping the worker alive beyond that deadline proves a disabled switch or
// departed phase cannot submit its previously scheduled background acceptance.
func TestAcceptDelayCancellation(t *testing.T) {
	for _, phaseChange := range []bool{false, true} {
		t.Run(fmt.Sprint(phaseChange), func(t *testing.T) {
			f := newAcceptFixture(t)
			f.runtime.state.Application.AcceptDelaySeconds = 1
			f.runtime.spawn(f.runtime.acceptLoop)
			deadline := time.Now().Add(time.Second)
			scheduled := false
			for time.Now().Before(deadline) {
				f.runtime.mu.Lock()
				scheduled = f.runtime.state.AcceptStatus == "匹配成功 · 1 秒后接受"
				f.runtime.mu.Unlock()
				if scheduled {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if !scheduled {
				t.Fatal("worker did not schedule the invitation")
			}
			if phaseChange {
				f.runtime.mu.Lock()
				f.runtime.state.Phase = "Lobby"
				f.runtime.readyCycle++
				f.runtime.mu.Unlock()
			} else {
				settings := f.runtime.ApplicationSettings()
				settings.AutoAccept = false
				if err := f.runtime.SaveApplicationSettings(settings); err != nil {
					t.Fatal(err)
				}
			}
			time.Sleep(1400 * time.Millisecond)
			if f.posts.Load() != 0 {
				t.Fatal("revoked deadline submitted acceptance")
			}
		})
	}
}

// TestApplicationPreferences restores the independent switch and bounds corrupted saved delays.
// This protects tray defaults and ensures saving acceptance never enables champion
// automation on a fresh runtime using the same preference directory.
func TestApplicationPreferences(t *testing.T) {
	t.Setenv("LOCALAPPDATA", t.TempDir())
	if settings := loadApplicationSettings(); !settings.CloseToTray || settings.AutoAccept || settings.AcceptDelaySeconds != 2 {
		t.Fatal(settings)
	}
	r := New(domain.Database{}, history.New(domain.Database{}))
	defer r.Close()
	settings := domain.ApplicationSettings{CloseToTray: false, AutoAccept: true, AcceptDelaySeconds: 3}
	if err := r.SaveApplicationSettings(settings); err != nil {
		t.Fatal(err)
	}
	if loadApplicationSettings() != settings {
		t.Fatal("preferences were not persisted")
	}
	if r.Snapshot().AutomationEnabled {
		t.Fatal("acceptance enabled champion automation")
	}
	settings.AcceptDelaySeconds = 99
	if r.SaveApplicationSettings(settings) == nil {
		t.Fatal("invalid delay accepted")
	}
	if err := savePreference("application.json", settings); err != nil {
		t.Fatal(err)
	}
	if loadApplicationSettings().AcceptDelaySeconds != 2 {
		t.Fatal("invalid persisted delay did not use bounded default")
	}
}
