//! Weekly schedule arithmetic and the "is a run due?" decision.
//!
//! The OS scheduler (launchd) starts Explo at the scheduled time, after wake if the Mac was
//! asleep, at login, and hourly as a safety net. Every start asks [`is_due`]; the answer is
//! "yes" at most once per scheduled slot, plus bounded retries if the run failed.

use chrono::{DateTime, Datelike, Duration, Local, NaiveDate, TimeZone, Utc};

use crate::config::ScheduleCfg;
use crate::state::State;

pub const MAX_ATTEMPTS_PER_SLOT: u32 = 6;
pub const WEEKDAYS: [&str; 7] = ["Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday", "Sunday"];

pub fn weekday_name(iso: u8) -> &'static str {
    WEEKDAYS[(iso.clamp(1, 7) - 1) as usize]
}

fn at(date: NaiveDate, s: &ScheduleCfg) -> Option<DateTime<Local>> {
    let naive = date.and_hms_opt(s.hour.min(23) as u32, s.minute.min(59) as u32, 0)?;
    Local.from_local_datetime(&naive).earliest()
}

/// The most recent scheduled instant at or before `now`.
pub fn last_due(now: DateTime<Local>, s: &ScheduleCfg) -> Option<DateTime<Local>> {
    (0..=7)
        .filter_map(|back| now.date_naive().checked_sub_signed(Duration::days(back)))
        .filter(|d| d.weekday().number_from_monday() as u8 == s.weekday)
        .filter_map(|d| at(d, s))
        .find(|t| *t <= now)
}

/// The next scheduled instant after `now`.
pub fn next_run(now: DateTime<Local>, s: &ScheduleCfg) -> Option<DateTime<Local>> {
    (0..=14)
        .filter_map(|fwd| now.date_naive().checked_add_signed(Duration::days(fwd)))
        .filter(|d| d.weekday().number_from_monday() as u8 == s.weekday)
        .filter_map(|d| at(d, s))
        .find(|t| *t > now)
}

#[derive(Debug, PartialEq, Eq)]
pub enum Due {
    Yes(DateTime<Utc>),
    No(String),
}

pub fn is_due(state: &State, now: DateTime<Local>, s: &ScheduleCfg) -> Due {
    let Some(slot) = last_due(now, s) else {
        return Due::No("no scheduled time has passed yet".into());
    };
    let slot_utc = slot.with_timezone(&Utc);
    if state.installed_at.is_some_and(|i| slot_utc < i) {
        return Due::No(format!("the {} slot is from before the schedule was installed; waiting for the next one", slot.format("%a %H:%M")));
    }
    if state.last_success.is_some_and(|ok| ok >= slot_utc) {
        return Due::No(format!("already ran successfully for the {} slot", slot.format("%a %d %b %H:%M")));
    }
    if state.slot == slot_utc.to_rfc3339() && state.attempts >= MAX_ATTEMPTS_PER_SLOT {
        return Due::No(format!("gave up on the {} slot after {} failed attempts; will try again next week", slot.format("%a %d %b"), state.attempts));
    }
    Due::Yes(slot_utc)
}

/// Escapes text for an XML `<string>` element.
fn xml(s: &str) -> String {
    s.replace('&', "&amp;").replace('<', "&lt;").replace('>', "&gt;")
}

/// launchd job: calendar slot + RunAtLoad + hourly safety net. Explo decides on each start whether a run is due.
pub fn launchd_plist(label: &str, exe: &str, s: &ScheduleCfg, log_path: &str, path_env: &str) -> String {
    format!(
        r#"<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>{label}</string>
    <key>ProgramArguments</key>
    <array>
        <string>{exe}</string>
        <string>--scheduled</string>
    </array>
    <key>StartCalendarInterval</key>
    <dict>
        <key>Weekday</key>
        <integer>{weekday}</integer>
        <key>Hour</key>
        <integer>{hour}</integer>
        <key>Minute</key>
        <integer>{minute}</integer>
    </dict>
    <key>StartInterval</key>
    <integer>3600</integer>
    <key>RunAtLoad</key>
    <true/>
    <key>EnvironmentVariables</key>
    <dict>
        <key>PATH</key>
        <string>{path}</string>
    </dict>
    <key>StandardOutPath</key>
    <string>{log}</string>
    <key>StandardErrorPath</key>
    <string>{log}</string>
</dict>
</plist>
"#,
        label = xml(label),
        exe = xml(exe),
        weekday = s.weekday.clamp(1, 7),
        hour = s.hour.min(23),
        minute = s.minute.min(59),
        path = xml(path_env),
        log = xml(log_path),
    )
}

/// The executable path recorded in a plist produced by [`launchd_plist`].
pub fn plist_exe(plist: &str) -> Option<String> {
    let after = plist.split("<key>ProgramArguments</key>").nth(1)?;
    let start = after.find("<string>")? + "<string>".len();
    let end = after[start..].find("</string>")? + start;
    Some(after[start..end].replace("&lt;", "<").replace("&gt;", ">").replace("&amp;", "&"))
}

#[cfg(test)]
mod tests {
    use super::*;

    fn local(y: i32, m: u32, d: u32, h: u32, min: u32) -> DateTime<Local> {
        Local.with_ymd_and_hms(y, m, d, h, min, 0).earliest().unwrap()
    }
    fn monday_9() -> ScheduleCfg {
        ScheduleCfg { weekday: 1, hour: 9, minute: 0 }
    }

    #[test]
    fn finds_the_most_recent_slot() {
        // 2026-11-23 is a Monday
        assert_eq!(last_due(local(2026, 11, 23, 9, 0), &monday_9()), Some(local(2026, 11, 23, 9, 0)));
        assert_eq!(last_due(local(2026, 11, 23, 8, 59), &monday_9()), Some(local(2026, 11, 16, 9, 0)));
        assert_eq!(last_due(local(2026, 11, 28, 12, 0), &monday_9()), Some(local(2026, 11, 23, 9, 0))); // Saturday
        assert_eq!(next_run(local(2026, 11, 28, 12, 0), &monday_9()), Some(local(2026, 11, 30, 9, 0)));
        assert_eq!(next_run(local(2026, 11, 23, 9, 0), &monday_9()), Some(local(2026, 11, 30, 9, 0)));
    }

    #[test]
    fn sunday_is_weekday_seven() {
        let s = ScheduleCfg { weekday: 7, hour: 20, minute: 30 };
        assert_eq!(last_due(local(2026, 11, 28, 12, 0), &s), Some(local(2026, 11, 22, 20, 30)));
        assert_eq!(weekday_name(7), "Sunday");
    }

    #[test]
    fn mac_asleep_through_monday_runs_on_wake() {
        let mut st = State { installed_at: Some(local(2026, 11, 1, 12, 0).with_timezone(&Utc)), last_success: Some(local(2026, 11, 16, 9, 5).with_timezone(&Utc)), ..Default::default() };
        // Monday 09:00 passes while the lid is closed; at 13:40 the Mac wakes and launchd starts us.
        assert!(matches!(is_due(&st, local(2026, 11, 23, 13, 40), &monday_9()), Due::Yes(_)));
        // ...and an hour later (hourly safety net) we have already succeeded: nothing to do.
        st.last_success = Some(local(2026, 11, 23, 13, 55).with_timezone(&Utc));
        assert!(matches!(is_due(&st, local(2026, 11, 23, 14, 40), &monday_9()), Due::No(_)));
        // all week long it stays quiet
        assert!(matches!(is_due(&st, local(2026, 11, 28, 10, 0), &monday_9()), Due::No(_)));
        // next Monday it is due again
        assert!(matches!(is_due(&st, local(2026, 11, 30, 9, 1), &monday_9()), Due::Yes(_)));
    }

    #[test]
    fn does_not_catch_up_slots_from_before_install() {
        let st = State { installed_at: Some(local(2026, 11, 25, 12, 0).with_timezone(&Utc)), ..Default::default() };
        assert!(matches!(is_due(&st, local(2026, 11, 26, 10, 0), &monday_9()), Due::No(_)));
        assert!(matches!(is_due(&st, local(2026, 11, 30, 9, 0), &monday_9()), Due::Yes(_)));
    }

    #[test]
    fn gives_up_after_repeated_failures_until_next_slot() {
        let now = local(2026, 11, 24, 10, 0);
        let slot = last_due(now, &monday_9()).unwrap().with_timezone(&Utc);
        let mut st = State { slot: slot.to_rfc3339(), attempts: 5, ..Default::default() };
        assert!(matches!(is_due(&st, now, &monday_9()), Due::Yes(_)));
        st.attempts = MAX_ATTEMPTS_PER_SLOT;
        assert!(matches!(is_due(&st, now, &monday_9()), Due::No(_)));
        // a new slot resets the budget (the stored slot no longer matches)
        assert!(matches!(is_due(&st, local(2026, 11, 30, 9, 30), &monday_9()), Due::Yes(_)));
    }

    #[test]
    fn plist_is_well_formed_and_escaped() {
        let p = launchd_plist("com.explo.weekly", "/Applications/Explo & Co/Explo.app/Contents/MacOS/explo", &ScheduleCfg { weekday: 1, hour: 9, minute: 5 }, "/tmp/l.log", "/opt/homebrew/bin:/usr/bin");
        assert!(p.contains("<string>/Applications/Explo &amp; Co/Explo.app/Contents/MacOS/explo</string>"));
        assert!(p.contains("<key>Weekday</key>\n        <integer>1</integer>"));
        assert!(p.contains("<key>Minute</key>\n        <integer>5</integer>"));
        assert!(p.contains("<key>RunAtLoad</key>\n    <true/>"));
        assert_eq!(plist_exe(&p).unwrap(), "/Applications/Explo & Co/Explo.app/Contents/MacOS/explo");
        // balanced tags
        assert_eq!(p.matches("<dict>").count(), p.matches("</dict>").count());
        assert_eq!(p.matches("<array>").count(), p.matches("</array>").count());
    }
}
