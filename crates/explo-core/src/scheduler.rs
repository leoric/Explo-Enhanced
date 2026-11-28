//! Installing / removing the recurring job with the operating system's scheduler.
//!
//! macOS: a per-user LaunchAgent. Unlike cron, launchd runs a calendar job that was missed
//! while the Mac slept as soon as it wakes. Other platforms: not implemented yet.

use anyhow::{bail, Result};
use std::path::{Path, PathBuf};

use crate::config::ScheduleCfg;

pub const LABEL: &str = "com.explo.weekly";

#[derive(Debug, Clone, Default)]
pub struct ScheduleStatus {
    pub supported: bool,
    pub installed: bool,
    pub loaded: bool,
    pub plist_path: Option<PathBuf>,
    /// Executable the installed job runs.
    pub exe: Option<String>,
    pub detail: String,
}

#[cfg(target_os = "macos")]
mod imp {
    use super::*;
    use crate::{config, schedule, state::State, tools};
    use anyhow::Context;
    use std::process::Command;

    fn plist_path() -> Result<PathBuf> {
        Ok(dirs::home_dir().context("no home directory")?.join("Library/LaunchAgents").join(format!("{LABEL}.plist")))
    }

    fn uid() -> Result<String> {
        let out = Command::new("id").arg("-u").output().context("running `id -u`")?;
        Ok(String::from_utf8_lossy(&out.stdout).trim().to_string())
    }

    fn launchctl(args: &[&str]) -> Result<(bool, String)> {
        let out = Command::new("launchctl").args(args).output().context("running launchctl")?;
        let text = format!("{}{}", String::from_utf8_lossy(&out.stdout), String::from_utf8_lossy(&out.stderr));
        Ok((out.status.success(), text.trim().to_string()))
    }

    pub fn install(s: &ScheduleCfg, exe: &Path) -> Result<()> {
        let path = plist_path()?;
        std::fs::create_dir_all(path.parent().unwrap())?;
        std::fs::create_dir_all(config::log_dir())?;
        let log = config::log_dir().join("launchd.log");
        let exe_str = exe.to_string_lossy().into_owned();
        let log_str = log.to_string_lossy().into_owned();
        let path_env = tools::path_env().to_string_lossy().into_owned();
        let xml = schedule::launchd_plist(LABEL, &exe_str, s, &log_str, &path_env);
        std::fs::write(&path, xml)?;

        let path_str: String = path.to_string_lossy().into_owned();
        let domain = format!("gui/{}", uid()?);
        let target = format!("{domain}/{LABEL}");
        let _ = launchctl(&["bootout", target.as_str()]); // not loaded yet is fine
        let mut st = State::load();
        st.installed_at = Some(chrono::Utc::now());
        st.save()?;
        let (ok, text) = launchctl(&["bootstrap", domain.as_str(), path_str.as_str()])?;
        if !ok {
            // older macOS versions
            let (ok2, text2) = launchctl(&["load", "-w", path_str.as_str()])?;
            if !ok2 {
                bail!("launchctl failed: {text} / {text2}");
            }
        }
        let _ = launchctl(&["enable", target.as_str()]);
        Ok(())
    }

    pub fn uninstall() -> Result<()> {
        let path = plist_path()?;
        let target = format!("gui/{}/{LABEL}", uid()?);
        let _ = launchctl(&["bootout", target.as_str()]);
        if path.exists() {
            std::fs::remove_file(&path)?;
        }
        Ok(())
    }

    pub fn status() -> ScheduleStatus {
        let mut st = ScheduleStatus { supported: true, ..Default::default() };
        let Ok(path) = plist_path() else { return st };
        if let Ok(text) = std::fs::read_to_string(&path) {
            st.installed = true;
            st.exe = schedule::plist_exe(&text);
        }
        st.plist_path = Some(path);
        if let Ok(u) = uid() {
            let target = format!("gui/{u}/{LABEL}");
            if let Ok((ok, text)) = launchctl(&["print", target.as_str()]) {
                st.loaded = ok;
                if !ok {
                    st.detail = text.lines().next().unwrap_or("").to_string();
                }
            }
        }
        st
    }
}

#[cfg(not(target_os = "macos"))]
mod imp {
    use super::*;

    pub fn install(_: &ScheduleCfg, _: &Path) -> Result<()> {
        bail!("Scheduling isn't implemented on this platform yet (macOS only for now)")
    }
    pub fn uninstall() -> Result<()> {
        bail!("Scheduling isn't implemented on this platform yet (macOS only for now)")
    }
    pub fn status() -> ScheduleStatus {
        ScheduleStatus { supported: false, detail: "Scheduling is only implemented for macOS so far".into(), ..Default::default() }
    }
}

/// Installs (or replaces) the weekly job so it runs `exe --scheduled`.
pub fn install(s: &ScheduleCfg, exe: &Path) -> Result<()> {
    imp::install(s, exe)
}
pub fn uninstall() -> Result<()> {
    imp::uninstall()
}
pub fn status() -> ScheduleStatus {
    imp::status()
}
