package updater

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// updater_test.go exercises public release handling and actual Windows replacement
// with isolated executable copies. No test contacts GitHub or modifies an installed
// Lux binary; the real worker test replaces only its own temporary child executable.

// TestMain dispatches copied native workers before the ordinary test runner starts.
// The opt-in child acts as a real exiting desktop owner. Its installer waits
// for that process to exit before replacing the isolated temporary executable.
func TestMain(m *testing.M) {
	if RunWorker(os.Args[1:]) {
		os.Exit(0)
	}
	if os.Getenv("LUX_UPDATER_TEST_PARENT") == "1" {
		self, _ := os.Executable()
		candidatePath := os.Getenv("LUX_UPDATER_TEST_CANDIDATE")
		digest := os.Getenv("LUX_UPDATER_TEST_DIGEST")
		pending := candidate{Directory: filepath.Dir(candidatePath), Path: candidatePath, Digest: digest, Version: "0.2.12"}
		if err := launchWorker(self, pending, false, os.Getenv("LUX_UPDATER_TEST_RESULT")); err != nil {
			fmt.Println(err)
			os.Exit(2)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// fixtureTransport implements in-memory public responses without changing URL validation rules.
type fixtureTransport func(*http.Request) (*http.Response, error)

// RoundTrip routes one public request through the owning test's controlled response.
func (f fixtureTransport) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

// reply creates a successful response that belongs to an isolated test client.
func reply(raw []byte) *http.Response {
	return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(raw)), Header: make(http.Header)}
}

// executableFixture copies this test image with a Windows GUI subsystem marker.
// Only temporary fixture bytes are changed. The resulting executable retains
// valid AMD64 headers and code so the production PE checks run unchanged.
func executableFixture(t *testing.T) []byte {
	t.Helper()
	self, _ := os.Executable()
	raw, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	peAt := int(binary.LittleEndian.Uint32(raw[0x3c:]))
	// PE32+ stores the subsystem after its fixed optional-header windows fields.
	binary.LittleEndian.PutUint16(raw[peAt+4+20+68:], 2)
	return raw
}

// digestFor computes the public asset digest format used by GitHub release responses.
func digestFor(raw []byte) string {
	hash := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(hash[:])
}

// fixtureRelease returns one stable, repository-scoped executable release observation.
func fixtureRelease(raw []byte) release {
	return release{Tag: "v0.2.12", Assets: []asset{{Name: "Lux.exe", URL: "https://github.com/zhengchalei/Lux/releases/download/v0.2.12/Lux.exe", Size: int64(len(raw)), Digest: digestFor(raw)}}}
}

// TestReleasePolicy protects numeric version comparison and exact release asset ownership.
// Preview releases, missing digests, changed repositories, and oversized assets must
// fail before an executable becomes available for automatic installation.
func TestReleasePolicy(t *testing.T) {
	if !newer("v0.2.10", "0.2.9") || newer("0.2.9", "0.2.10") || newer("0.2.12-beta", "0.2.11") || newer("0.2.11", "0.2.11") {
		t.Fatal("stable numeric comparison failed")
	}
	for _, address := range []string{"http://github.com/file", "https://github.com.evil.example/file", "https://u:p@github.com/file", "https://github.com:443/file"} {
		if trustedURL(address) {
			t.Fatal("untrusted URL accepted", address)
		}
	}
	for _, scenario := range []string{"valid", "prerelease", "missing", "digest", "repository", "size", "older"} {
		t.Run(scenario, func(t *testing.T) {
			entry := fixtureRelease([]byte("fixture"))
			switch scenario {
			case "prerelease":
				entry.Prerelease = true
			case "missing":
				entry.Assets = nil
			case "digest":
				entry.Assets[0].Digest = ""
			case "repository":
				entry.Assets[0].URL = "https://github.com/someone/Lux/releases/download/v0.2.12/Lux.exe"
			case "size":
				entry.Assets[0].Size = maxAsset + 1
			case "older":
				entry.Tag = "v0.2.9"
			}
			raw, _ := json.Marshal(entry)
			client := &http.Client{Transport: fixtureTransport(func(req *http.Request) (*http.Response, error) {
				if req.URL.String() != latestURL || req.Header.Get("User-Agent") == "" || req.Header.Get("Authorization") != "" {
					t.Error("public metadata contract changed")
				}
				return reply(raw), nil
			})}
			version, item, err := latest(context.Background(), client, "0.2.11")
			if scenario == "valid" {
				if err != nil || item == nil || version != "0.2.12" {
					t.Fatalf("valid release rejected: %s %v", version, err)
				}
				return
			}
			if scenario == "older" {
				if err != nil || item != nil || version != "0.2.11" {
					t.Fatal("older release offered a downgrade")
				}
				return
			}
			if err == nil {
				t.Fatal("invalid release accepted")
			}
		})
	}
}

// TestDownloadValidation exercises actual staged bytes, digest mismatches, and invalid PE content.
// Failed downloads remove their partial file; only verified Windows x64 content
// may remain ready, and no installed executable participates in the test.
func TestDownloadValidation(t *testing.T) {
	image := executableFixture(t)
	for _, scenario := range []string{"valid", "truncated", "digest", "not-executable", "oversized"} {
		t.Run(scenario, func(t *testing.T) {
			raw := image
			item := fixtureRelease(raw).Assets[0]
			switch scenario {
			case "truncated":
				raw = raw[:len(raw)/2]
			case "digest":
				item.Digest = digestFor([]byte("different"))
			case "not-executable":
				raw = []byte("<html>gateway</html>")
				item.Size, item.Digest = int64(len(raw)), digestFor(raw)
			case "oversized":
				item.Size--
			}
			client := &http.Client{Transport: fixtureTransport(func(*http.Request) (*http.Response, error) { return reply(raw), nil })}
			path := filepath.Join(t.TempDir(), "pending.exe")
			err := download(context.Background(), client, item, path, nil)
			_, statErr := os.Stat(path)
			if scenario == "valid" {
				if err != nil || statErr != nil {
					t.Fatal(err, statErr)
				}
			} else if err == nil || !os.IsNotExist(statErr) {
				t.Fatalf("failed candidate survived: %v %v", err, statErr)
			}
		})
	}
}

// waitService waits for bounded asynchronous work to finish before inspecting filesystem effects.
func waitService(t *testing.T, service *Service) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		service.mu.Lock()
		busy := service.busy
		service.mu.Unlock()
		if !busy {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("update service did not finish")
}

// TestServiceDownloadAndCancellation checks duplicate admission, ready reuse, and persisted stops.
// Disabling during a blocked metadata request must cancel work without offering a
// stale download; re-enabling starts a fresh request after that cancellation completes.
func TestServiceDownloadAndCancellation(t *testing.T) {
	image := executableFixture(t)
	entry, _ := json.Marshal(fixtureRelease(image))
	s := New(context.Background(), "0.2.11", t.TempDir(), "isolated.exe")
	defer s.Close()
	var downloads atomic.Int32
	s.http = &http.Client{Transport: fixtureTransport(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() == latestURL {
			return reply(entry), nil
		}
		downloads.Add(1)
		return reply(image), nil
	})}
	s.Check()
	s.Check()
	waitService(t, s)
	if s.Status().State != "ready" || downloads.Load() != 1 {
		t.Fatal("verified update was not ready", s.Status())
	}
	s.Check()
	waitService(t, s)
	if downloads.Load() != 1 {
		t.Fatal("ready candidate was downloaded twice")
	}
	if err := s.SetEnabled(false); err != nil {
		t.Fatal(err)
	}
	if s.pending != nil || s.Status().Enabled {
		t.Fatal("disable retained pending installation")
	}
	if restored := New(context.Background(), "0.2.11", s.directory, "isolated.exe"); restored.Status().Enabled {
		t.Fatal("disabled setting was not restored")
	}
	entered := make(chan struct{}, 1)
	var calls atomic.Int32
	s.http = &http.Client{Transport: fixtureTransport(func(req *http.Request) (*http.Response, error) {
		if calls.Add(1) == 1 {
			entered <- struct{}{}
			<-req.Context().Done()
			return nil, req.Context().Err()
		}
		return reply([]byte(`{"tag_name":"v0.2.11","assets":[]}`)), nil
	})}
	if err := s.SetEnabled(true); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("check was not admitted")
	}
	if err := s.SetEnabled(false); err != nil {
		t.Fatal(err)
	}
	if err := s.SetEnabled(true); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && s.Status().State != "current" {
		time.Sleep(10 * time.Millisecond)
	}
	if s.Status().State != "current" || calls.Load() != 2 {
		t.Fatal("re-enable lost fresh check", s.Status(), calls.Load())
	}
}

// TestReplacementRollback protects the old executable when replacement or requested restart fails.
// Both errors are induced in an isolated temporary directory. A successful plain
// exit replacement changes the target and consumes the temporary backup.
func TestReplacementRollback(t *testing.T) {
	for _, scenario := range []string{"missing-candidate", "launch-failure", "success"} {
		t.Run(scenario, func(t *testing.T) {
			directory := t.TempDir()
			target, prepared := filepath.Join(directory, "Lux.exe"), filepath.Join(directory, "new.exe")
			os.WriteFile(target, []byte("old"), 0600)
			if scenario != "missing-candidate" {
				os.WriteFile(prepared, []byte("new"), 0600)
			}
			err := replaceExecutable(target, prepared, scenario == "launch-failure")
			raw, _ := os.ReadFile(target)
			if scenario == "success" {
				if err != nil || string(raw) != "new" {
					t.Fatal(err, string(raw))
				}
			} else if err == nil || string(raw) != "old" {
				t.Fatal("old executable was not restored", err, string(raw))
			}
		})
	}
}

// TestNativeWorkerReplacesAfterExit exercises the real helper with a real exiting process.
// The parent executable and candidate live under a temporary path containing spaces
// and Chinese text, and the resulting target must match the exact staged digest.
func TestNativeWorkerReplacesAfterExit(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "Lux 更新 fixture")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	self, _ := os.Executable()
	target := filepath.Join(directory, "Lux.exe")
	if err := copyFile(self, target); err != nil {
		t.Fatal(err)
	}
	image := executableFixture(t)
	candidatePath := filepath.Join(directory, "pending.exe")
	os.WriteFile(candidatePath, image, 0700)
	result := filepath.Join(directory, "result.json")
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, target)
	command.Env = append(os.Environ(), "LUX_UPDATER_TEST_PARENT=1", "LUX_UPDATER_TEST_CANDIDATE="+candidatePath, "LUX_UPDATER_TEST_DIGEST="+digestFor(image), "LUX_UPDATER_TEST_RESULT="+result)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("native parent failed: %v %s", err, output)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		raw, err := os.ReadFile(result)
		if err == nil {
			if strings.Contains(string(raw), `"error":""`) {
				if err = verifyDigest(target, digestFor(image)); err != nil {
					t.Fatal(err)
				}
				return
			}
			t.Fatal("worker failed", string(raw))
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal(errors.New("worker never reported replacement"))
}
