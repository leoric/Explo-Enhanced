//! Locating and running yt-dlp / ffmpeg.
//!
//! Apps started from Finder or launchd get a minimal PATH that doesn't include Homebrew,
//! so we search the usual install locations as well.

use anyhow::{anyhow, bail, Result};
use std::io::Read;
use std::path::{Path, PathBuf};
use std::process::{Command, Stdio};
use std::sync::atomic::{AtomicBool, Ordering};
use std::time::{Duration, Instant};

use crate::config::DownloadCfg;
use crate::events::Cancelled;

fn extra_dirs() -> Vec<PathBuf> {
    let mut dirs: Vec<PathBuf> = ["/opt/homebrew/bin", "/usr/local/bin", "/opt/local/bin", "/usr/bin", "/bin"].iter().map(PathBuf::from).collect();
    if let Some(home) = dirs::home_dir() {
        dirs.push(home.join(".local/bin"));
        dirs.push(home.join("bin"));
    }
    dirs
}

/// PATH value for child processes: the current PATH plus the usual extra locations.
pub fn path_env() -> std::ffi::OsString {
    let mut parts: Vec<PathBuf> = std::env::var_os("PATH").map(|p| std::env::split_paths(&p).collect()).unwrap_or_default();
    for d in extra_dirs() {
        if !parts.contains(&d) {
            parts.push(d);
        }
    }
    std::env::join_paths(parts).unwrap_or_default()
}

/// Resolves a tool: the configured path if set, else PATH + usual locations.
pub fn find(name: &str, configured: &str) -> Option<PathBuf> {
    let configured = configured.trim();
    if !configured.is_empty() {
        let p = PathBuf::from(configured);
        return p.is_file().then_some(p);
    }
    let exe = format!("{name}{}", std::env::consts::EXE_SUFFIX);
    let path = path_env();
    std::env::split_paths(&path).map(|d| d.join(&exe)).find(|p| p.is_file())
}

/// ffprobe lives next to ffmpeg.
pub fn find_ffprobe(ffmpeg: &Path) -> Option<PathBuf> {
    let sibling = ffmpeg.with_file_name(format!("ffprobe{}", std::env::consts::EXE_SUFFIX));
    sibling.is_file().then_some(sibling).or_else(|| find("ffprobe", ""))
}

#[derive(Debug, Clone)]
pub struct ToolStatus {
    pub name: &'static str,
    pub path: Option<PathBuf>,
    pub version: Option<String>,
}

pub fn check(cfg: &DownloadCfg) -> Vec<ToolStatus> {
    let ytdlp = find("yt-dlp", &cfg.ytdlp_path);
    let ffmpeg = find("ffmpeg", &cfg.ffmpeg_path);
    let ffprobe = ffmpeg.as_deref().and_then(find_ffprobe);
    let status = |name: &'static str, path: Option<PathBuf>, flag: &str| {
        let version = path.as_ref().and_then(|p| {
            let mut c = Command::new(p);
            c.arg(flag);
            run_capture(c, Duration::from_secs(10), None).ok().and_then(|o| o.stdout.lines().next().map(|l| l.split_whitespace().take(3).collect::<Vec<_>>().join(" ")))
        });
        ToolStatus { name, path, version }
    };
    vec![status("yt-dlp", ytdlp, "--version"), status("ffmpeg", ffmpeg, "-version"), status("ffprobe", ffprobe, "-version")]
}

/// A `Command` for `program` with the extended PATH.
pub fn command(program: &Path) -> Command {
    let mut c = Command::new(program);
    c.env("PATH", path_env());
    c
}

#[derive(Debug)]
pub struct Captured {
    pub success: bool,
    pub stdout: String,
    pub stderr: String,
}

/// Runs a command with a timeout, capturing output. Kills the child on timeout or cancel.
pub fn run_capture(mut cmd: Command, timeout: Duration, cancel: Option<&AtomicBool>) -> Result<Captured> {
    cmd.stdin(Stdio::null()).stdout(Stdio::piped()).stderr(Stdio::piped());
    let mut child = cmd.spawn().map_err(|e| anyhow!("couldn't start {:?}: {e}", cmd.get_program()))?;
    let mut so = child.stdout.take().ok_or_else(|| anyhow!("no stdout"))?;
    let mut se = child.stderr.take().ok_or_else(|| anyhow!("no stderr"))?;
    let t_out = std::thread::spawn(move || {
        let mut b = Vec::new();
        let _ = so.read_to_end(&mut b);
        b
    });
    let t_err = std::thread::spawn(move || {
        let mut b = Vec::new();
        let _ = se.read_to_end(&mut b);
        b
    });
    let start = Instant::now();
    let status = loop {
        if let Some(st) = child.try_wait()? {
            break st;
        }
        if start.elapsed() > timeout {
            let _ = child.kill();
            let _ = child.wait();
            bail!("timed out after {}s", timeout.as_secs());
        }
        if cancel.is_some_and(|c| c.load(Ordering::Relaxed)) {
            let _ = child.kill();
            let _ = child.wait();
            return Err(Cancelled.into());
        }
        std::thread::sleep(Duration::from_millis(150));
    };
    Ok(Captured {
        success: status.success(),
        stdout: String::from_utf8_lossy(&t_out.join().unwrap_or_default()).into_owned(),
        stderr: String::from_utf8_lossy(&t_err.join().unwrap_or_default()).into_owned(),
    })
}

/// Last non-empty line of stderr: usually the actual error message.
pub fn last_line(s: &str) -> String {
    s.lines().rev().map(str::trim).find(|l| !l.is_empty()).unwrap_or("no output").to_string()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    #[cfg(unix)]
    fn captures_output_and_times_out() {
        let mut c = Command::new("sh");
        c.args(["-c", "echo hi; echo oops >&2"]);
        let out = run_capture(c, Duration::from_secs(5), None).unwrap();
        assert!(out.success);
        assert_eq!(out.stdout.trim(), "hi");
        assert_eq!(last_line(&out.stderr), "oops");

        let mut slow = Command::new("sleep");
        slow.arg("5");
        let err = run_capture(slow, Duration::from_millis(300), None).unwrap_err();
        assert!(err.to_string().contains("timed out"));
    }

    #[test]
    #[cfg(unix)]
    fn cancel_kills_the_child() {
        let flag = AtomicBool::new(true);
        let mut slow = Command::new("sleep");
        slow.arg("5");
        let err = run_capture(slow, Duration::from_secs(10), Some(&flag)).unwrap_err();
        assert!(err.downcast_ref::<Cancelled>().is_some());
    }

    #[test]
    fn configured_path_must_exist() {
        assert!(find("whatever", "/definitely/not/here").is_none());
    }
}
