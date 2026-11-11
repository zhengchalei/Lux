//! lib.rs exposes Lux's data models, selection rules, and client data services.
//! These modules are independent of window creation, allowing protocol, source
//! isolation, and contribution calculations to be checked without a GPU window.
pub mod automation;
pub mod client;
pub mod history;
pub mod model;
pub mod scouting;
pub mod sources;
