//! diagnostics.rs measures native frame construction and synthetic pointer or scroll dispatch in an opt-in build.
//! A warmup lets normal client queries populate the selected page before a
//! bounded redraw workload starts. GPUI's own histograms measure complete window
//! drawing and invalidation-to-presentation time, so the report does not confuse
//! a background data poll interval with the application's rendering capacity.
//!
//! Warmup and the frame workload request redraws only within bounded windows;
//! input workloads rely on hover or scrolling invalidations from dispatch. Each
//! subtracts the initial histograms, then writes a counts-only report outside
//! the source tree. Ordinary builds omit this module and do not run a continuous
//! redraw loop; diagnostic builds also remain event-driven without the explicit
//! command-line option. Pointer measurements use GPUI's public input dispatcher
//! from a separate asynchronous window task, after the rendering borrow ends.
//! They describe synthetic GPUI input rather than physical Windows input or
//! hardware cursor latency. Reports contain no player identities or client tokens.

use gpui_kit::{
    App, Modifiers, MouseMoveEvent, PlatformInput, ScrollDelta, ScrollEvent, ScrollSource,
    TouchPhase, Window, point, profiler::FrameDurationSnapshot, px,
};
use serde_json::{Value, json};
use std::time::{Duration, Instant};

// Pointer cadence and duration bound the asynchronous workload while allowing normal client events and presentation between injected moves.
const POINTER_INTERVAL: Duration = Duration::from_millis(8);
const POINTER_DURATION: Duration = Duration::from_secs(6);

/// Workload selects repeated drawing, asynchronous pointer movement, or list scrolling.
#[derive(Clone, Copy, PartialEq, Eq)]
enum Workload {
    Frames,  // Existing continuous root redraw capacity measurement.
    Pointer, // GPUI input dispatch with normal hover handling.
    Scroll,  // GPUI scroll dispatch within visible list content.
}

/// FrameProbe owns one bounded diagnostic workload and its initial histogram snapshot.
pub(super) struct FrameProbe {
    tag: String,                                      // Local report filename suffix.
    workload: Workload, // Explicitly selected diagnostic workload kind.
    launched: Instant,  // Warmup start monotonic time.
    sample: Option<(Instant, FrameDurationSnapshot)>, // Sample baseline recorded after warmup.
    complete: bool,     // Stops requesting frames after reporting.
}

impl FrameProbe {
    /// from_args enables a measurement only for an explicit local diagnostic argument.
    /// The sanitized tag chooses the artifact name. Invalid or absent tags leave
    /// the diagnostic build idle without changing the ordinary application behavior.
    pub(super) fn from_args(args: &[String]) -> Option<Self> {
        let index = args.iter().position(|arg| {
            matches!(
                arg.as_str(),
                "--frame-probe" | "--pointer-probe" | "--scroll-probe"
            )
        })?;
        let tag = args.get(index + 1)?;
        if tag.is_empty() || !tag.chars().all(|c| c.is_ascii_alphanumeric() || c == '-') {
            return None;
        }
        Some(Self {
            tag: tag.clone(),
            workload: match args[index].as_str() {
                "--pointer-probe" => Workload::Pointer,
                "--scroll-probe" => Workload::Scroll,
                _ => Workload::Frames,
            },
            launched: Instant::now(),
            sample: None,
            complete: false,
        })
    }

    /// tick advances data warmup, samples repeated draws, and persists the completed report.
    /// Call it from the root view's render so the frame workload rebuilds that
    /// subtree and input warmup observes the same data. After eight seconds,
    /// populated query or roster pages begin sampling; thirty seconds bounds
    /// unavailable-data waits.
    ///
    /// Histogram subtraction excludes startup and warmup costs. The retained
    /// context records actual loaded players and samples, so the report describes
    /// its measured workload rather than assuming every request already finished.
    /// An input workload transfers ownership to one bounded asynchronous task.
    /// Its first window update occurs after an awaited timer, preventing input
    /// dispatch from reentering this root render or borrowing the view twice.
    /// All workloads measure application work separately from physical input.
    pub(super) fn tick(&mut self, window: &mut Window, cx: &mut App, context: Value) {
        if self.complete {
            return;
        }
        let elapsed = self.launched.elapsed();
        let page = context["page"].as_u64().unwrap_or(0);
        let players = &context["players"];
        let roster = players["rosterPlayers"].as_u64().unwrap_or(0);
        let loaded = players["loadedProfiles"].as_u64().unwrap_or(0);
        let waiting = (page == 3 && (roster == 0 || loaded < roster))
            || (page == 2 && players["queryRecords"].as_u64().unwrap_or(0) == 0);
        // Wait for real page data before measuring; the upper bound still produces an explicit empty-workload report when connectivity is unavailable.
        if elapsed < Duration::from_secs(8) || (waiting && elapsed < Duration::from_secs(30)) {
            window.request_animation_frame();
            return;
        }
        if self.workload != Workload::Frames {
            // Transfer the one-shot workload before spawning so later root renders cannot launch another input task or extend warmup indefinitely.
            self.complete = true;
            start_input_probe(
                window,
                cx,
                self.workload,
                self.tag.clone(),
                elapsed,
                context,
            );
            return;
        }
        let (started, before) = self
            .sample
            .get_or_insert_with(|| (Instant::now(), window.frame_duration_snapshot()));
        // Five seconds provide hundreds of redraw opportunities without leaving a permanent rendering loop running after diagnosis.
        if started.elapsed() < Duration::from_secs(5) {
            window.request_animation_frame();
            return;
        }
        let after = window.frame_duration_snapshot();
        let mut draw = after.draw_duration_histogram;
        let mut dirty = after.dirty_to_present_histogram;
        let mut interval = after.present_interval_histogram;
        // Baseline subtraction keeps the measurements local to this workload even when warmup rendered the same page many times.
        let subtracted = draw.subtract(&before.draw_duration_histogram).is_ok()
            && dirty.subtract(&before.dirty_to_present_histogram).is_ok()
            && interval
                .subtract(&before.present_interval_histogram)
                .is_ok();
        let report = json!({
            "workload":"continuous root redraw after bounded data warmup",
            "warmupSeconds":started.duration_since(self.launched).as_secs_f64(),
            "tag":self.tag,"histogramsSubtracted":subtracted,
            "elapsedSeconds":started.elapsed().as_secs_f64(),"frames":draw.len(),
            "gpuSpecs":window.gpu_specs(),
            "refreshIntervalMs":window.refresh_interval(cx).map(|interval|interval.as_secs_f64()*1000.),
            "windowActiveAtEnd":window.is_window_active(),
            "legacyFrameLimits":std::env::args().any(|argument|argument=="--legacy-frame-limits"),
            "drawMs":{"median":draw.value_at_quantile(0.5) as f64/1e6,"p95":draw.value_at_quantile(0.95) as f64/1e6,"max":draw.max() as f64/1e6},
            "dirtyToPresentMs":{"median":dirty.value_at_quantile(0.5) as f64/1e6,"p95":dirty.value_at_quantile(0.95) as f64/1e6},
            "presentIntervalMs":{"median":interval.value_at_quantile(0.5) as f64/1e6,"p95":interval.value_at_quantile(0.95) as f64/1e6},
            "identitiesStored":false
        });
        let mut report = report;
        report["context"] = context;
        self.complete = true;
        // Filesystem errors leave the probe stopped; diagnostic output failure must not keep the shipping UI repainting indefinitely.
        let _ = std::fs::create_dir_all("artifacts").and_then(|_| {
            std::fs::write(
                format!("artifacts/frame-{}.json", self.tag),
                report.to_string(),
            )
        });
    }
}

/// start_input_probe measures finite pointer movement or scrolling through GPUI input dispatch.
/// The caller may invoke it while rendering because the task awaits before
/// accessing its window. Every event runs in AsyncWindowContext::update, with
/// no pressed button, click, keyboard action, or business command. Scroll mode
/// moves the selected list and intentionally leaves its final offset unchanged.
///
/// Snapshot subtraction excludes startup and warmup, and dispatch duration is
/// measured separately from the frame work that follows it. The final short
/// timer lets the last event-driven frame present before histograms are read;
/// the task then restores GPUI's original pointer position and stops scheduling.
/// Dispatch alone decides whether hover or scrolling changes invalidate a view. No redraw
/// is forced, so local caching remains visible in the measured workload and
/// asynchronous code never requires GPUI's rendering-only current-view stack.
/// Region build counters use the same boundaries as timing snapshots, exposing
/// root, workspace, and card reconstruction in cached and uncached comparisons.
fn start_input_probe(
    window: &mut Window,
    cx: &mut App,
    workload: Workload,
    tag: String,
    warmup: Duration,
    context: Value,
) {
    window
        .spawn(cx, async move |cx| {
            // Yield out of the root render before borrowing its window or dispatching events; this also bounds each turn of foreground work.
            cx.background_executor().timer(POINTER_INTERVAL).await;
            let Ok((before, before_input, before_builds, original, gpu, refresh_interval, active_at_start)) =
                cx.update(|window, cx| {
                    (
                        window.frame_duration_snapshot(),
                        window.input_latency_snapshot(),
                        super::regions::build_counts(),
                        window.mouse_position(),
                        window.gpu_specs(),
                        window.refresh_interval(cx),
                        window.is_window_active(),
                    )
                })
            else {
                return;
            };
            let started = Instant::now();
            let mut dispatch_ns = Vec::new();
            let mut active_events = 0_u64;
            while started.elapsed() < POINTER_DURATION {
                let step = dispatch_ns.len();
                let Ok((duration, active)) = cx.update(|window, cx| {
                    let size = window.viewport_size();
                    let input = if workload == Workload::Scroll {
                        // Reverse every eighty events so repeated eighteen-pixel moves exercise both directions rather than remaining at one list boundary.
                        let direction = if (step / 80) % 2 == 0 { -18. } else { 18. };
                        PlatformInput::Scroll(ScrollEvent {
                            position: point(size.width * 0.3, size.height * 0.65),
                            delta: ScrollDelta::Pixels(point(px(0.), px(direction))),
                            touch_phase: TouchPhase::Moved,
                            source: ScrollSource::Pointer,
                            modifiers: Modifiers::default(),
                        })
                    } else {
                        let (x, y) = pointer_position(step, size.width.as_f32(), size.height.as_f32());
                        PlatformInput::MouseMove(MouseMoveEvent {
                            position: point(px(x), px(y)),
                            pressed_button: None,
                            modifiers: Modifiers::default(),
                        })
                    };
                    let start = Instant::now();
                    window.dispatch_event(input, cx);
                    (start.elapsed(), window.is_window_active())
                }) else {
                    return;
                };
                dispatch_ns.push(duration.as_nanos().min(u64::MAX as u128) as u64);
                active_events += u64::from(active);
                cx.background_executor().timer(POINTER_INTERVAL).await;
            }
            let elapsed = started.elapsed();
            // Allow the final input invalidation to present before sampling; this settling delay never forces a redraw or restarts injection.
            cx.background_executor()
                .timer(Duration::from_millis(32))
                .await;
            let Ok((after, after_input, after_builds)) = cx.update(|window, cx| {
                let snapshots = (
                    window.frame_duration_snapshot(),
                    window.input_latency_snapshot(),
                    super::regions::build_counts(),
                );
                // Restore only GPUI's hover position after capturing the sample, keeping the user's real system pointer and business state untouched.
                window.dispatch_event(
                    PlatformInput::MouseMove(MouseMoveEvent {
                        position: original,
                        pressed_button: None,
                        modifiers: Modifiers::default(),
                    }),
                    cx,
                );
                snapshots
            }) else {
                return;
            };
            let mut draw = after.draw_duration_histogram;
            let mut dirty = after.dirty_to_present_histogram;
            let mut interval = after.present_interval_histogram;
            let mut latency = after_input.latency_histogram;
            let mut events = after_input.events_per_frame_histogram;
            // Subtract each independent histogram even when another subtraction fails, exposing an explicit validity flag instead of mixing warmup into successful deltas.
            let subtractions = [
                draw.subtract(&before.draw_duration_histogram).is_ok(),
                dirty.subtract(&before.dirty_to_present_histogram).is_ok(),
                interval
                    .subtract(&before.present_interval_histogram)
                    .is_ok(),
                latency.subtract(&before_input.latency_histogram).is_ok(),
                events
                    .subtract(&before_input.events_per_frame_histogram)
                    .is_ok(),
            ];
            dispatch_ns.sort_unstable();
            let dispatch_quantile = |quantile: f64| {
                let index = (dispatch_ns.len() as f64 * quantile).ceil() as usize;
                dispatch_ns.get(index.saturating_sub(1)).copied().unwrap_or(0)
            };
            let scroll = workload == Workload::Scroll;
            let prefix = if scroll { "scroll" } else { "pointer" };
            let report = json!({
                "workload":if scroll {"synthetic GPUI scrolling with event-driven list redraw after bounded data warmup"}else{"synthetic GPUI pointer input with event-driven hover redraw after bounded data warmup"},
                "inputPath":if scroll {"fixed viewport position (0.3 width, 0.65 height); pixel delta y=-18/+18, reversed every 80 events"}else{"interpolated navigation, player-card, and recent-outcome hover path"},
                "artifactPrefix":prefix,
                "physicalWindowsInput":false,
                "tag":tag,"context":context,"identitiesStored":false,
                "uncachedComparison":std::env::args().any(|argument|argument=="--uncached-ui"),
                "legacyFrameLimits":std::env::args().any(|argument|argument=="--legacy-frame-limits"),
                "windowActiveAtStart":active_at_start,"activeDispatchEvents":active_events,
                "buildCounts":{"root":after_builds[0].saturating_sub(before_builds[0]),"workspace":after_builds[1].saturating_sub(before_builds[1]),"scoutCards":after_builds[2].saturating_sub(before_builds[2])},
                "warmupSeconds":warmup.as_secs_f64(),"elapsedSeconds":elapsed.as_secs_f64(),
                "targetIntervalMs":POINTER_INTERVAL.as_secs_f64()*1000.,
                "dispatchEvents":dispatch_ns.len(),"frames":draw.len(),
                "histogramsSubtracted":subtractions.into_iter().all(|ok|ok),
                "dispatchMs":timing_summary(dispatch_ns.len() as u64,dispatch_quantile(0.5),dispatch_quantile(0.95),dispatch_quantile(1.)),
                "drawMs":timing_summary(draw.len(),draw.value_at_quantile(0.5),draw.value_at_quantile(0.95),draw.max()),
                "dirtyToPresentMs":timing_summary(dirty.len(),dirty.value_at_quantile(0.5),dirty.value_at_quantile(0.95),dirty.max()),
                "presentIntervalMs":timing_summary(interval.len(),interval.value_at_quantile(0.5),interval.value_at_quantile(0.95),interval.max()),
                "presentIntervalScope":"GPUI samples continuous animation only; zero samples means unavailable for event-driven input",
                "inputLatencyMs":timing_summary(latency.len(),latency.value_at_quantile(0.5),latency.value_at_quantile(0.95),latency.max()),
                "inputLatencyScope":"GPUI records only input dispatches that invalidate the window; OS delivery and physical cursor latency are excluded",
                "eventsPerFrame":{"samples":events.len(),"median":events.value_at_quantile(0.5),"p95":events.value_at_quantile(0.95),"max":events.max()},
                "midDrawEventsDropped":after_input.mid_draw_events_dropped.saturating_sub(before_input.mid_draw_events_dropped),
                "gpuSpecs":gpu,"refreshIntervalMs":refresh_interval.map(|interval|interval.as_secs_f64()*1000.),
            });
            // Reporting happens after sampling and never schedules further frames, including when the artifact directory cannot be written.
            let _ = std::fs::create_dir_all("artifacts").and_then(|_| {
                std::fs::write(format!("artifacts/{prefix}-{tag}.json"), report.to_string())
            });
        })
        .detach();
}

/// pointer_position traverses navigation, player cards, and outcome strips in logical pixels.
/// The path scales with the viewport and never leaves its drawable bounds.
/// Smooth segments alternate both teams and five card heights, crossing hover
/// boundaries without activating controls or depending on private player data.
fn pointer_position(step: usize, width: f32, height: f32) -> (f32, f32) {
    let row = (step / 72) % 5;
    let y = (270. + row as f32 * 116.).min((height - 72.).max(20.));
    let targets = [
        (80., 152. + row as f32 * 26.),
        (width * 0.26, y - 24.),
        (width * 0.49, y + 28.),
        (width * 0.74, y - 24.),
        (width * 0.94, y + 28.),
        (80., 152. + row as f32 * 26.),
    ];
    // Eighteen samples per segment repeatedly cross card and tooltip boundaries, while five target segments complete one horizontal sweep.
    let segment = (step / 18) % (targets.len() - 1);
    let progress = (step % 18) as f32 / 18.;
    let from = targets[segment];
    let to = targets[segment + 1];
    let x = from.0 + (to.0 - from.0) * progress;
    let y = from.1 + (to.1 - from.1) * progress;
    (
        x.clamp(1., (width - 1.).max(1.)),
        y.clamp(1., (height - 1.).max(1.)),
    )
}

/// timing_summary converts nanosecond timing statistics into explicit millisecond report values.
fn timing_summary(samples: u64, median: u64, p95: u64, max: u64) -> Value {
    json!({"samples":samples,"median":median as f64/1e6,"p95":p95 as f64/1e6,"max":max as f64/1e6})
}
