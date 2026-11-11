//! sgp.rs selects verified game servers from League Akari's public configuration.
//! Server addresses and region path identifiers come from that configuration,
//! because local client region aliases cannot safely manufacture backend URLs.
//! The embedded file is a public baseline fetched from LeagueAkari-Config; a
//! validated newer cached or remote document may replace it at runtime.
//!
//! Only same-region history is selected here. Tencent's platform code is
//! required to distinguish its servers, while known non-Tencent client aliases
//! map to configuration keys. Remote configuration contains no client secrets,
//! and authentication stays in the requesting repository. Every configured URL
//! must use HTTPS before it can receive a bearer token.

use anyhow::{Context, Result, bail};
use serde::{Deserialize, Serialize};
use std::collections::BTreeMap;

/// CONFIG_URLS identifies the two public mirrors used by Akari's configuration repository.
pub(super) const CONFIG_URLS: [&str; 2] = [
    "https://raw.githubusercontent.com/LeagueAkari/LeagueAkari-Config/refs/heads/main/config/sgp/league-servers.json",
    "https://gitee.com/LeagueAkari/LeagueAkari-Config/raw/main/config/sgp/league-servers.json",
];

/// Directory contains only the public server fields needed for match-history routing.
#[derive(Clone, Deserialize, Serialize)]
pub(super) struct Directory {
    pub version: u64, // Public configuration schema version.
    #[serde(rename = "lastUpdate")]
    pub last_update: u64, // Publisher's configuration revision timestamp.
    pub servers: BTreeMap<String, Server>, // Exact configured server identifiers.
}

/// Server describes one configured history origin and its optional path identifier.
#[derive(Clone, Deserialize, Serialize)]
pub(super) struct Server {
    #[serde(rename = "matchHistory")]
    pub match_history: String, // Configured HTTPS match history origin.
    #[serde(rename = "regionPathParam")]
    pub region_path: Option<String>, // Publisher-specified match identifier prefix.
}

impl Directory {
    /// embedded parses the bundled public configuration without requiring network connectivity.
    pub fn embedded() -> Result<Self> {
        Self::parse(include_bytes!("sgp-servers.json"))
    }

    /// parse validates the supported schema and every history URL before selection.
    /// Empty origins mean unsupported servers. Other non-HTTPS or credential-bearing
    /// origins reject the document so a remote update cannot redirect bearer tokens.
    pub fn parse(bytes: &[u8]) -> Result<Self> {
        let directory: Self = serde_json::from_slice(bytes)?;
        if directory.version > 2 || directory.servers.is_empty() {
            bail!("Unsupported SGP server configuration");
        }
        for server in directory.servers.values() {
            if server.match_history.is_empty() {
                continue;
            }
            let url = reqwest::Url::parse(&server.match_history)?;
            if url.scheme() != "https"
                || url.host_str().is_none()
                || !url.username().is_empty()
                || url.password().is_some()
                || url.query().is_some()
                || url.fragment().is_some()
            {
                bail!("Invalid SGP history origin");
            }
        }
        Ok(directory)
    }

    /// select resolves a local region and platform into one supported history origin.
    /// Display labels may include a slash-separated platform, which is stripped
    /// before alias lookup. Tencent selection requires an explicit platform and
    /// does not broaden requests to other Tencent servers when it is unavailable.
    pub fn select(&self, region: &str, platform: Option<&str>) -> Result<(Server, String)> {
        let mut parts = region.split('/').map(str::trim);
        let region = parts.next().unwrap_or("").to_ascii_uppercase();
        let platform = platform
            .or_else(|| parts.next())
            .unwrap_or("")
            .to_ascii_uppercase();
        let server_id = if region == "TENCENT" {
            if platform.is_empty() {
                bail!("SGP 大区平台信息未就绪");
            }
            format!("TENCENT_{platform}")
        } else {
            match region.as_str() {
                "NA" => "NA1",
                "BR" => "BR1",
                "TR" => "TR1",
                "LAN" => "LA1",
                "LAS" => "LA2",
                "OCE" => "OC1",
                "EUW1" => "EUW",
                "JP1" => "JP",
                _ => &region,
            }
            .to_owned()
        };
        // Public revisions use both NA and NA1 keys; retain the verified backend path while selecting only their configured equivalent origin.
        let legacy_key = match server_id.as_str() {
            "NA1" => "NA",
            "BR1" => "BR",
            "TR1" => "TR",
            "LA1" => "LAN",
            "LA2" => "LAS",
            "OC1" => "OCE",
            "EUW" => "EUW1",
            "JP" => "JP1",
            _ => &server_id,
        };
        let server = self
            .servers
            .get(&server_id)
            .or_else(|| self.servers.get(&region))
            .or_else(|| self.servers.get(legacy_key))
            .filter(|s| !s.match_history.is_empty())
            .context("当前大区未配置 SGP 战绩接口")?
            .clone();
        let path = server.region_path.clone().unwrap_or_else(|| {
            server_id
                .strip_prefix("TENCENT_")
                .unwrap_or(&server_id)
                .to_owned()
        });
        Ok((server, path))
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    /// region_aliases_resolve_only_configured_servers protects local-to-server routing contracts.
    /// Tencent platform identity remains explicit and a missing platform cannot select
    /// an arbitrary server, while NA aliases use the configured same-region origin.
    #[test]
    fn region_aliases_resolve_only_configured_servers() {
        let directory = Directory::embedded().unwrap();
        assert_eq!(directory.select("NA", None).unwrap().1, "NA1");
        assert_eq!(directory.select("TENCENT / HN1", None).unwrap().1, "HN1");
        assert!(directory.select("TENCENT", None).is_err());
        assert!(directory.select("UNKNOWN", None).is_err());
    }

    /// public_configuration_rejects_unsafe_token_origins protects bearer-token destination validation.
    /// A remote configuration using plaintext HTTP must fail before any authenticated
    /// request is built, rather than silently replacing a previously valid directory.
    #[test]
    fn public_configuration_rejects_unsafe_token_origins() {
        assert!(Directory::parse(br#"{"version":2,"lastUpdate":1,"servers":{"NA1":{"matchHistory":"http://example.com"}}}"#).is_err());
    }
}
