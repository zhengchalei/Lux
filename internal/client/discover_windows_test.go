//go:build windows

package client

import "testing"

// discover_windows_test.go checks credential parsing without reading real processes.
// Fixtures distinguish Riot service flags from League's local endpoint and keep
// invalid ports and unsupported lockfile protocols out of authenticated transports.

// TestDiscoveryFlagIsolation reproduces quoted League flags next to unrelated Riot ports.
// Exact flag names must preserve the League endpoint and optional platform independently.
func TestDiscoveryFlagIsolation(t *testing.T) {
	access, ok := parseArguments(`"C:\LeagueClientUx.exe" --riotclient-app-port=9999 "--app-port=12345" "--remoting-auth-token=test-value" --rso_platform_id=NJ100`)
	if !ok || access.Port != 12345 || access.Token != "test-value" || access.Platform != "NJ100" {
		t.Fatal("local endpoint flags were confused with Riot metadata")
	}
	if _, ok := parseArguments(`--riotclient-app-port=9999 --riotclient-auth-token=unrelated`); ok {
		t.Fatal("Riot service flags became League credentials")
	}
}

// TestRiotFlagIsolation requires Riot credentials independently from League flags.
func TestRiotFlagIsolation(t *testing.T) {
	riot, ok := parseRiotArguments(`--app-port=123 --remoting-auth-token=league --riotclient-app-port=456 --riotclient-auth-token=riot-value`)
	if !ok || riot.port != 456 || riot.token != "riot-value" {
		t.Fatal("Riot and League credentials were mixed")
	}
	for _, command := range []string{`--app-port=123 --remoting-auth-token=league`, `--riotclient-app-port=65536 --riotclient-auth-token=riot-value`, `--riotclient-app-port=456`} {
		if _, ok := parseRiotArguments(command); ok {
			t.Fatal("incomplete Riot access accepted")
		}
	}
}

// TestLockfileContractRejectsInvalidPorts reproduces incomplete and unsupported credential records.
// Only the documented five-field HTTPS format and valid TCP port may enter discovery.
func TestLockfileContractRejectsInvalidPorts(t *testing.T) {
	for _, raw := range []string{"", "League:1:0:test:https", "League:1:65536:test:https", "League:1:123:test:http", "League:1:123::https", "League:1:123:test:https:extra"} {
		if _, ok := parseLockfile(raw); ok {
			t.Fatal("invalid lockfile accepted")
		}
	}
	if access, ok := parseLockfile("League:1:12345:test:https\n"); !ok || access.Port != 12345 {
		t.Fatal("valid credential record rejected")
	}
}
