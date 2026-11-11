#![cfg_attr(not(debug_assertions), windows_subsystem = "windows")]

//! main.rs starts Lux as a native GPUI Kit desktop application.
//! The application initializes the component library before constructing its
//! first window, so controls share the toolkit theme and asset provider.
//! Four workspace icons supplement the default component assets without embedding
//! the complete icon catalog, keeping navigation available in the portable binary.
//! Window updates follow the display cadence when demanded, including hover on
//! an unfocused window. Disabling GPUI's inactive and battery-saver frame caps
//! avoids their default thirty-frame limit without adding an idle redraw loop.

mod ui;
use gpui_kit::component::{Theme, ThemeMode};
use gpui_kit::*;

// These selected icons supplement the toolkit defaults and remain embedded for offline Windows navigation and automation state presentation.
assets::icon_assets!(WorkspaceIcons, [ChartBar, Swords, Users, ShieldCheck]);

/// AppAssets combines the toolkit controls with the four workspace-specific icons.
struct AppAssets;

impl AssetSource for AppAssets {
    /// load resolves workspace icons before consulting the default component catalog.
    /// Unknown paths retain the toolkit's normal missing-asset behavior, while
    /// selected icons borrow embedded bytes without filesystem access or downloads.
    fn load(&self, path: &str) -> Result<Option<std::borrow::Cow<'static, [u8]>>> {
        if let Some(bytes) = WorkspaceIcons.load(path)? {
            return Ok(Some(bytes));
        }
        assets::Assets.load(path)
    }

    /// list returns the combined icon paths matching the requested asset prefix.
    /// Sorting and deduplication give consumers stable enumeration even when the
    /// toolkit adds one of the workspace icons to its default catalog later.
    fn list(&self, path: &str) -> Result<Vec<SharedString>> {
        let mut paths = assets::Assets.list(path)?;
        paths.extend(WorkspaceIcons.list(path)?);
        paths.sort();
        paths.dedup();
        Ok(paths)
    }
}

/// main initializes GPUI Kit and opens Lux's native application window.
/// Startup normally activates the window; --no-activate leaves focus with the
/// current application. Diagnostic-only flags restore legacy limits for comparison.
fn main() {
    application().with_assets(AppAssets).run(|cx| {
        init(cx);
        Theme::change(ThemeMode::Light, None, cx);
        #[cfg(feature = "diagnostics")]
        let legacy_limits = std::env::args().any(|arg| arg == "--legacy-frame-limits");
        #[cfg(not(feature = "diagnostics"))]
        let legacy_limits = false;
        #[cfg(feature = "diagnostics")]
        let focus =
            !std::env::args().any(|arg| arg == "--probe-inactive-window" || arg == "--no-activate");
        #[cfg(not(feature = "diagnostics"))]
        let focus = !std::env::args().any(|arg| arg == "--no-activate");
        let bounds = Bounds::centered(None, size(px(1280.), px(860.)), cx);
        let options = WindowOptions {
            // Mouse hover need not focus the window or meet GPUI's input-rate threshold; remove both optional thirty-frame caps while preserving event-driven rendering.
            inactive_frame_interval: legacy_limits
                .then(|| std::time::Duration::from_micros(33_333)),
            low_power_frame_interval: legacy_limits
                .then(|| std::time::Duration::from_micros(33_333)),
            focus,
            window_bounds: Some(WindowBounds::Windowed(bounds)),
            window_min_size: Some(size(px(1060.), px(700.))),
            titlebar: Some(TitlebarOptions {
                title: Some("Lux · 英雄联盟战绩助手".into()),
                ..Default::default()
            }),
            ..Default::default()
        };
        open_window(options, cx, |window, cx| {
            cx.new(|cx| ui::Lux::new(window, cx))
        })
        .expect("Unable to open Lux window");
        cx.on_window_closed(|cx, _| {
            if cx.windows().is_empty() {
                cx.quit();
            }
        })
        .detach();
        if focus {
            cx.activate(true);
        }
    });
}
