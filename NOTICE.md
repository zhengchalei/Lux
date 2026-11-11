# Third-party sources and dependencies

Lux 0.2.8 is an independent Windows application using MyGo, Go, and Vue. It is not endorsed by Riot Games, OP.GG, RESG, or Bilibili. Original Lux source uses the MIT license in LICENSE; third-party materials retain their own licenses and rights. Runtime dependency license texts accompany this distribution in THIRD_PARTY_LICENSES.txt.

## Game artwork and observations

- League of Legends champion names and artwork belong to Riot Games. Bundled portraits are resized JPEG derivatives of Riot's [Data Dragon 16.22.1 champion icons](https://ddragon.leagueoflegends.com/cdn/16.22.1/img/champion/).
- Public champion observations are attributed to [RESG](https://www.bilibili.com/toy/resg/index.html) and [OP.GG](https://www.op.gg/) in the interface. Bundled observations are snapshots. Source region, tier, mode, patch, sample size, and collection time remain relevant when interpreting them.
- LCU/SGP protocol routing references the user-provided LeagueAkari source. The embedded public server directory comes from [LeagueAkari-Config](https://github.com/LeagueAkari/LeagueAkari-Config/blob/main/config/sgp/league-servers.json). Public configuration and statistics requests do not receive private client credentials.
- LeagueAkari is MIT licensed, copyright 2026 Hanxven. The separately maintained LeagueAkari-Config repository is GPL-3.0 licensed; its copied server directory remains under that license in `internal/history/sgp-servers.json` and the historical Rust copy `src/providers/sgp-servers.json`. The full configuration source and license are available at [LeagueAkari-Config](https://github.com/LeagueAkari/LeagueAkari-Config), and its GPL text is included in THIRD_PARTY_LICENSES.txt.

## Desktop runtime and frontend

- [MyGo 0.8.3](https://github.com/egoist/mygo/tree/v0.8.3) and mygo-runtime 0.8.3: MIT license, copyright 2026 MyGo contributors. [License](https://github.com/egoist/mygo/blob/v0.8.3/LICENSE).
- [Vue 3.5.45](https://github.com/vuejs/core): MIT license. [License](https://github.com/vuejs/core/blob/main/LICENSE).
- [Gorilla WebSocket 1.5.3](https://github.com/gorilla/websocket/tree/v1.5.3): BSD-style license, copyright 2013 The Gorilla WebSocket Authors. [License](https://github.com/gorilla/websocket/blob/v1.5.3/LICENSE).
- Other Go dependencies, including purego and Go supplementary libraries, retain their respective licenses. Exact module versions and checksums are recorded in `go.mod` and `go.sum`.
- Vite, TypeScript, vue-tsc, the Vue Vite plugin, and their dependencies retain their respective licenses. Exact frontend and build dependency versions are recorded in `frontend/package-lock.json`. Node.js and frontend build tools are not required on the end user's machine.
- Microsoft Edge WebView2 Runtime is a Microsoft component under its applicable terms. It is installed separately or supplied by Windows; Lux's MyGo portable package does not bundle WebView2.

## Retained Rust source

The earlier Rust + GPUI Kit 0.9.1 implementation, Cargo manifests, and historical verification artifacts remain in the repository for migration comparisons. They are not linked into or launched by the MyGo executable. Their dependency versions remain recorded in `Cargo.lock`, and their respective licenses continue to apply to that source and any separately retained Rust package.

The old Rust portable package's Microsoft `vcruntime140.dll` notice applies to that package. The MyGo build script does not copy this library into the new portable package.
