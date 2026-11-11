package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"lux/internal/domain"
)

// browser.go serves the opt-in loopback browser development entrypoint for Lux.
// It shares the desktop application methods and compiled frontend, allowing real
// Go service integration to be tested before packaging. Desktop execution never
// starts this server. Cross-origin requests are rejected before invoking product
// actions, and only explicit named methods are reachable through JSON POSTs.

// serveBrowser runs the explicit local development endpoint until cancellation or failure.
// Only loopback addresses are accepted. The same frontend can therefore exercise
// real data without publishing the client API on a network interface.
func serveBrowser(ctx context.Context, address string, files fs.FS, app *App) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	if host != "127.0.0.1" && host != "localhost" && host != "::1" {
		return errors.New("开发服务仅支持本机地址")
	}
	mux := http.NewServeMux()
	mux.Handle("/", mygo.FileServer(files))
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST required", 405)
			return
		}
		origin := r.Header.Get("Origin")
		if origin != "" && origin != "http://"+r.Host {
			http.Error(w, "origin rejected", 403)
			return
		}
		if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			http.Error(w, "JSON required", 415)
			return
		}
		var args []json.RawMessage
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64*1024)).Decode(&args); err != nil {
			http.Error(w, "invalid JSON", 400)
			return
		}
		result, err := callBrowser(r.Context(), app, strings.TrimPrefix(r.URL.Path, "/api/"), args)
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		if err != nil {
			_ = json.NewEncoder(w).Encode(map[string]any{"error": err.Error()})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"result": result})
	})
	server := &http.Server{Addr: address, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	return server.ListenAndServe()
}

// argument decodes one positional API argument and rejects omitted or malformed values.
func argument[T any](args []json.RawMessage, index int) (T, error) {
	var v T
	if index >= len(args) {
		return v, errors.New("缺少请求参数")
	}
	err := json.Unmarshal(args[index], &v)
	return v, err
}

// callBrowser dispatches only the explicit desktop method contract for development tests.
// Decode failures return before the associated product action runs. Arbitrary
// method names cannot invoke reflection helpers or internal service operations.
func callBrowser(ctx context.Context, a *App, method string, args []json.RawMessage) (any, error) {
	switch method {
	case "SaveApplicationSettings":
		settings, err := argument[domain.ApplicationSettings](args, 0)
		if err != nil {
			return nil, err
		}
		return nil, a.SaveApplicationSettings(settings)
	case "GetDesktopStatus":
		return a.GetDesktopStatus(), nil
	case "SetDraftPanelCollapsed":
		collapsed, e := argument[bool](args, 0)
		if e != nil {
			return nil, e
		}
		return nil, a.SetDraftPanelCollapsed(collapsed)
	case "GetDraftSnapshot":
		return a.GetDraftSnapshot(), nil
	case "GetDraftPanelStatus":
		return a.GetDraftPanelStatus(), nil
	case "SetDraftPanel":
		enabled, e := argument[bool](args, 0)
		if e != nil {
			return nil, e
		}
		pinned, e := argument[bool](args, 1)
		if e != nil {
			return nil, e
		}
		return nil, a.SetDraftPanel(enabled, pinned)
	case "GetDraftSkins", "SelectDraftChampion", "SetDraftSkin", "GetSkinPreview":
		key, e := argument[string](args, 0)
		if e != nil {
			return nil, e
		}
		champion, e := argument[int](args, 1)
		if e != nil {
			return nil, e
		}
		if method == "GetDraftSkins" {
			return a.GetDraftSkins(ctx, key, champion)
		}
		target, e := argument[int](args, 2)
		if e != nil {
			return nil, e
		}
		if method == "GetSkinPreview" {
			return a.GetSkinPreview(ctx, key, champion, target)
		}
		if method == "SetDraftSkin" {
			return nil, a.SetDraftSkin(ctx, key, champion, target)
		}
		return nil, a.SelectDraftChampion(ctx, key, champion, target)
	case "GetSnapshot":
		return a.GetSnapshot(), nil
	case "GetAISettings":
		return a.GetAISettings(), nil
	case "SaveAISettings":
		input, e := argument[domain.AISettingsInput](args, 0)
		if e != nil {
			return nil, e
		}
		return a.SaveAISettings(input)
	case "CancelAIAnalysis":
		requestID, e := argument[string](args, 0)
		if e != nil {
			return nil, e
		}
		a.CancelAIAnalysis(requestID)
		return nil, nil
	case "GetTimeline", "AnalyzeTimeline":
		puuid, e := argument[string](args, 0)
		if e != nil {
			return nil, e
		}
		id, e := argument[uint64](args, 1)
		if e != nil {
			return nil, e
		}
		opponent, e := argument[int](args, 2)
		if e != nil {
			return nil, e
		}
		if method == "GetTimeline" {
			return a.GetTimeline(ctx, puuid, id, opponent)
		}
		requestID, e := argument[string](args, 3)
		if e != nil {
			return nil, e
		}
		return a.AnalyzeTimeline(ctx, puuid, id, opponent, requestID)
	case "GetRevision":
		return a.GetRevision(), nil
	case "Lookup":
		name, e := argument[string](args, 0)
		if e != nil {
			return nil, e
		}
		q, e := argument[int](args, 1)
		if e != nil {
			return nil, e
		}
		return nil, a.Lookup(name, q)
	case "SelectSummoner":
		id, e := argument[uint64](args, 0)
		if e != nil {
			return nil, e
		}
		puuid, e := argument[string](args, 1)
		if e != nil {
			return nil, e
		}
		queue, e := argument[int](args, 2)
		if e != nil {
			return nil, e
		}
		return nil, a.SelectSummoner(id, puuid, queue)
	case "GetSummonerIcon":
		icon, e := argument[int](args, 0)
		if e != nil {
			return nil, e
		}
		return a.GetSummonerIcon(ctx, icon)
	case "QuerySelf":
		q, e := argument[int](args, 0)
		if e != nil {
			return nil, e
		}
		return nil, a.QuerySelf(q)
	case "ReviewLastGame":
		return nil, a.ReviewLastGame()
	case "RefreshBattle":
		return nil, a.RefreshBattle()
	case "RetryScout":
		id, e := argument[string](args, 0)
		if e != nil {
			return nil, e
		}
		return nil, a.RetryScout(id)
	case "SetAnalysis":
		count, e := argument[int](args, 0)
		if e != nil {
			return nil, e
		}
		same, e := argument[bool](args, 1)
		if e != nil {
			return nil, e
		}
		return nil, a.SetAnalysis(count, same)
	case "SaveAutomation":
		settings, e := argument[domain.AutomationSettings](args, 0)
		if e != nil {
			return nil, e
		}
		enabled, e := argument[bool](args, 1)
		if e != nil {
			return nil, e
		}
		return nil, a.SaveAutomation(settings, enabled)
	case "StopAutomation":
		a.StopAutomation()
		return nil, nil
	case "GetMatchDetail", "GetReplay", "ReplayAction":
		id, e := argument[string](args, 0)
		if e != nil {
			return nil, e
		}
		match, e := argument[uint64](args, 1)
		if e != nil {
			return nil, e
		}
		if method == "GetReplay" {
			return a.GetReplay(ctx, id, match)
		}
		if method == "ReplayAction" {
			action, e := argument[string](args, 2)
			if e != nil {
				return nil, e
			}
			return nil, a.ReplayAction(ctx, id, match, action)
		}
		return a.GetMatchDetail(ctx, id, match)
	case "GetMatchups":
		id, e := argument[int](args, 0)
		if e != nil {
			return nil, e
		}
		lane, e := argument[string](args, 1)
		if e != nil {
			return nil, e
		}
		return a.GetMatchups(ctx, id, lane)
	case "InspectChampion":
		id, e := argument[int](args, 0)
		if e != nil {
			return nil, e
		}
		return nil, a.InspectChampion(ctx, id)
	}
	return nil, fmt.Errorf("未知应用方法")
}
