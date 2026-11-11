//! regions.rs gives independent native surfaces their own cached view boundaries.
//! GPUI associates hover invalidation with the nearest rendered entity. Building
//! navigation, every player card, and the active workspace inside one entity
//! therefore makes a small hover transition rebuild unrelated combat content.
//! These persistent regions delegate presentation to Lux while retaining separate
//! GPUI identities, so clean neighboring regions can reuse layout and paint.
//!
//! Lux still owns account, room, selection, and request identities. An explicit
//! Lux notification invalidates its regions before the next frame, preventing a
//! cached player slot from retaining an earlier room's data or click handler.
//! Regions reference their owner weakly and keep their observation subscriptions
//! alive with the view. Slot coordinates select current visible participants;
//! player requests themselves continue to use the current account identity.

use super::*;

// Diagnostic counters compare complete card construction across the same pointer path without collecting player observations or retaining rendered elements.
#[cfg(feature = "diagnostics")]
thread_local! {
    static BUILD_COUNTS: std::cell::Cell<[u64; 3]> = const { std::cell::Cell::new([0; 3]) };
}

/// record_build counts root, workspace, or player-card construction for diagnostic comparisons.
#[cfg(feature = "diagnostics")]
pub(super) fn record_build(index: usize) {
    BUILD_COUNTS.with(|counts| {
        let mut next = counts.get();
        next[index] += 1;
        counts.set(next);
    });
}

/// build_counts returns root, workspace, and card construction totals on this UI thread.
#[cfg(feature = "diagnostics")]
pub(super) fn build_counts() -> [u64; 3] {
    BUILD_COUNTS.with(std::cell::Cell::get)
}

/// RegionKind identifies a persistent surface without retaining player snapshots.
#[derive(Clone, Copy)]
pub(super) enum RegionKind {
    Sidebar,   // Persistent application navigation surface.
    Header,    // Current account and connection surface.
    Workspace, // Currently selected product page surface.
    Footer,    // Operation and public-source status surface.
    Scout {
        ally: bool,   // Current participant's displayed team orientation.
        index: usize, // Zero-based position within that displayed side.
    },
}

/// Region delegates rendering while isolating its native hover and paint invalidation.
pub(super) struct Region {
    owner: WeakEntity<Lux>, // Weak presentation owner avoids a retained entity cycle.
    kind: RegionKind,       // Stable location rendered by this view.
    _subscription: Subscription, // Owner notifications invalidate this cached surface.
}

impl Region {
    /// new associates one cached surface with its owner's explicit data notifications.
    /// Pointer invalidation belongs to this region instead of notifying Lux.
    /// Lux notifications invalidate all regions, keeping account, navigation, and
    /// selection changes coherent without cloning the retained player observations.
    /// The subscription does not translate local hover changes back into owner
    /// notifications, which would invalidate all neighboring regions and defeat
    /// the boundary. A room change instead arrives through the owner first, so
    /// reused card slots receive fresh data before their cached tree is reused.
    fn new(owner: &Entity<Lux>, kind: RegionKind, cx: &mut Context<Self>) -> Self {
        // Keep owner-driven updates separate from local hover notifications so hovering one surface cannot invalidate every neighboring cached surface.
        let subscription = cx.observe(owner, |_, _, cx| cx.notify());
        Self {
            owner: owner.downgrade(),
            kind,
            _subscription: subscription,
        }
    }
}

impl Render for Region {
    /// render builds the current surface under its own native invalidation identity.
    /// The owner update supplies the existing typed click listeners. A released
    /// owner produces an empty surface while the closing window tears down views.
    fn render(&mut self, window: &mut Window, cx: &mut Context<Self>) -> impl IntoElement {
        let element = self
            .owner
            .update(cx, |owner, cx| owner.render_region(self.kind, window, cx))
            // A released owner means window teardown is underway; return an empty surface instead of retaining stale content during that teardown.
            .unwrap_or_else(|_| div().into_any_element());
        div()
            .size_full()
            .flex_1()
            .min_h_0()
            .flex()
            .flex_col()
            .child(element)
    }
}

/// Regions retains the surfaces shared by root layout and the ten-player workspace.
pub(super) struct Regions {
    pub sidebar: Entity<Region>,   // Persistent navigation and theme surface.
    pub header: Entity<Region>,    // Account and connection status surface.
    pub workspace: Entity<Region>, // Active page with independent hover invalidation.
    pub footer: Entity<Region>,    // Operation and source freshness surface.
    pub scouts: [[Entity<Region>; 5]; 2], // Friendly then opposing five-slot cached surfaces.
}

impl Regions {
    /// new allocates stable view identities before the first native layout pass.
    /// The fixed card slots resolve their current visible player at render time.
    /// Owner notifications refresh reused slots when the account or room changes.
    pub(super) fn new(owner: &Entity<Lux>, cx: &mut App) -> Self {
        Self {
            sidebar: cx.new(|cx| Region::new(owner, RegionKind::Sidebar, cx)),
            header: cx.new(|cx| Region::new(owner, RegionKind::Header, cx)),
            workspace: cx.new(|cx| Region::new(owner, RegionKind::Workspace, cx)),
            footer: cx.new(|cx| Region::new(owner, RegionKind::Footer, cx)),
            scouts: std::array::from_fn(|side| {
                std::array::from_fn(|index| {
                    cx.new(|cx| {
                        Region::new(
                            owner,
                            RegionKind::Scout {
                                ally: side == 0,
                                index,
                            },
                            cx,
                        )
                    })
                })
            }),
        }
    }
}
