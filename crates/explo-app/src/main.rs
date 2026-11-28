//! Explo: GUI by default; `--scheduled` / `--run` run headless (used by the weekly launchd job).

mod gui;

use explo_core::events::{Ctx, Event, Level};
use explo_core::{config::Config, logfile, pipeline, scheduler};
use std::process::ExitCode;

const HELP: &str = "\
Explo — weekly music discovery for your self-hosted library

USAGE:
    explo                 open the app
    explo --scheduled     headless: run only if this week's run is due (what the weekly job does)
    explo --run           headless: run all enabled playlists now
    explo --install       install the weekly schedule (macOS LaunchAgent)
    explo --uninstall     remove the weekly schedule
    explo --status        show schedule status
    explo --version
";

fn headless_ctx() -> Ctx {
    Ctx::new(|e| {
        if let Event::Log(level, msg) = e {
            if level >= Level::Info {
                eprintln!("{msg}");
            }
            logfile::append(level, &msg);
        }
    })
}

fn main() -> ExitCode {
    let args: Vec<String> = std::env::args().skip(1).collect();
    match args.first().map(String::as_str) {
        None => gui::run(),
        Some("--scheduled") => {
            let ctx = headless_ctx();
            match pipeline::scheduled(&ctx, false) {
                Ok(_) => ExitCode::SUCCESS,
                Err(e) => {
                    ctx.error(format!("{e:#}"));
                    ExitCode::FAILURE
                }
            }
        }
        Some("--run") => {
            let ctx = headless_ctx();
            match pipeline::scheduled(&ctx, true) {
                Ok(_) => ExitCode::SUCCESS,
                Err(e) => {
                    ctx.error(format!("{e:#}"));
                    ExitCode::FAILURE
                }
            }
        }
        Some("--install") => match (Config::load(), std::env::current_exe()) {
            (Ok(cfg), Ok(exe)) => match scheduler::install(&cfg.schedule, &exe) {
                Ok(()) => {
                    println!("Installed. Runs {} at {:02}:{:02}.", explo_core::schedule::weekday_name(cfg.schedule.weekday), cfg.schedule.hour, cfg.schedule.minute);
                    ExitCode::SUCCESS
                }
                Err(e) => {
                    eprintln!("{e:#}");
                    ExitCode::FAILURE
                }
            },
            (Err(e), _) => {
                eprintln!("{e:#}");
                ExitCode::FAILURE
            }
            (_, Err(e)) => {
                eprintln!("{e}");
                ExitCode::FAILURE
            }
        },
        Some("--uninstall") => match scheduler::uninstall() {
            Ok(()) => {
                println!("Schedule removed.");
                ExitCode::SUCCESS
            }
            Err(e) => {
                eprintln!("{e:#}");
                ExitCode::FAILURE
            }
        },
        Some("--status") => {
            let s = scheduler::status();
            println!("supported: {}\ninstalled: {}\nloaded:    {}\nexecutable: {}", s.supported, s.installed, s.loaded, s.exe.unwrap_or_default());
            ExitCode::SUCCESS
        }
        Some("--version") | Some("-V") => {
            println!("explo {}", env!("CARGO_PKG_VERSION"));
            ExitCode::SUCCESS
        }
        Some("--help") | Some("-h") => {
            print!("{HELP}");
            ExitCode::SUCCESS
        }
        // macOS passes "-psn_..." when launching from Finder on old systems; anything else is a mistake
        Some(a) if a.starts_with("-psn_") => gui::run(),
        Some(other) => {
            eprintln!("unknown option {other}\n\n{HELP}");
            ExitCode::from(2)
        }
    }
}
