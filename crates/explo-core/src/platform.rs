//! Small OS-specific helpers.

use std::path::Path;
use std::process::{Child, Command, Stdio};

/// Keeps the Mac from idle-sleeping while a run is in progress (macOS `caffeinate`). No-op elsewhere.
pub struct KeepAwake(Option<Child>);

impl KeepAwake {
    pub fn new() -> Self {
        #[cfg(target_os = "macos")]
        {
            let child = Command::new("caffeinate").args(["-i", "-w", &std::process::id().to_string()]).stdin(Stdio::null()).stdout(Stdio::null()).stderr(Stdio::null()).spawn().ok();
            return KeepAwake(child);
        }
        #[allow(unreachable_code)]
        KeepAwake(None)
    }
}

impl Default for KeepAwake {
    fn default() -> Self {
        Self::new()
    }
}

impl Drop for KeepAwake {
    fn drop(&mut self) {
        if let Some(c) = &mut self.0 {
            let _ = c.kill();
            let _ = c.wait();
        }
    }
}

/// Desktop notification (macOS only for now).
pub fn notify(title: &str, message: &str) {
    #[cfg(target_os = "macos")]
    {
        let esc = |s: &str| s.replace('\\', "\\\\").replace('"', "\\\"");
        let script = format!("display notification \"{}\" with title \"{}\"", esc(message), esc(title));
        let _ = Command::new("osascript").args(["-e", &script]).stdin(Stdio::null()).stdout(Stdio::null()).stderr(Stdio::null()).status();
    }
    #[cfg(not(target_os = "macos"))]
    let _ = (title, message);
}

/// Opens a file or folder with the default application.
pub fn open(path: &Path) {
    #[cfg(target_os = "macos")]
    let prog = "open";
    #[cfg(target_os = "windows")]
    let prog = "explorer";
    #[cfg(not(any(target_os = "macos", target_os = "windows")))]
    let prog = "xdg-open";
    let _ = Command::new(prog).arg(path).stdin(Stdio::null()).stdout(Stdio::null()).stderr(Stdio::null()).spawn();
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn keep_awake_is_harmless() {
        let _a = KeepAwake::new();
    }
}
