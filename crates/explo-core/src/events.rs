//! Progress reporting and cancellation, shared by the GUI and the headless runner.

use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::Arc;

#[derive(Clone, Copy, Debug, PartialEq, Eq, PartialOrd, Ord)]
pub enum Level {
    Debug,
    Info,
    Warn,
    Error,
}

#[derive(Clone, Debug)]
pub enum Event {
    Log(Level, String),
    /// What the run is doing right now ("Downloading tracks", ...).
    Stage(String),
    Progress { done: usize, total: usize },
}

/// Returned (and downcastable) when the user cancels a run.
#[derive(Debug)]
pub struct Cancelled;
impl std::fmt::Display for Cancelled {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        write!(f, "cancelled")
    }
}
impl std::error::Error for Cancelled {}

#[derive(Clone)]
pub struct Ctx {
    cancel: Arc<AtomicBool>,
    emit: Arc<dyn Fn(Event) + Send + Sync>,
}

impl Ctx {
    pub fn new(emit: impl Fn(Event) + Send + Sync + 'static) -> Self {
        Self { cancel: Arc::new(AtomicBool::new(false)), emit: Arc::new(emit) }
    }
    /// A context that drops all events (tests, connection checks).
    pub fn silent() -> Self {
        Self::new(|_| {})
    }
    pub fn cancel_handle(&self) -> Arc<AtomicBool> {
        self.cancel.clone()
    }
    pub fn cancelled(&self) -> bool {
        self.cancel.load(Ordering::Relaxed)
    }
    pub fn check(&self) -> anyhow::Result<()> {
        if self.cancelled() {
            Err(Cancelled.into())
        } else {
            Ok(())
        }
    }
    pub fn log(&self, level: Level, msg: impl Into<String>) {
        (self.emit)(Event::Log(level, msg.into()));
    }
    pub fn debug(&self, msg: impl Into<String>) {
        self.log(Level::Debug, msg)
    }
    pub fn info(&self, msg: impl Into<String>) {
        self.log(Level::Info, msg)
    }
    pub fn warn(&self, msg: impl Into<String>) {
        self.log(Level::Warn, msg)
    }
    pub fn error(&self, msg: impl Into<String>) {
        self.log(Level::Error, msg)
    }
    pub fn stage(&self, msg: impl Into<String>) {
        (self.emit)(Event::Stage(msg.into()));
    }
    pub fn progress(&self, done: usize, total: usize) {
        (self.emit)(Event::Progress { done, total });
    }
}
