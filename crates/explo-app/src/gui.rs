//! The Explo window: dashboard, settings, schedule and log.

use chrono::Local;
use eframe::egui::{self, Color32, RichText};
use explo_core::config::{Config, NameFormat, PLAYLIST_KINDS};
use explo_core::events::{Ctx, Event, Level};
use explo_core::pipeline::{self, Trigger};
use explo_core::platform::{self, KeepAwake};
use explo_core::schedule::{self, WEEKDAYS};
use explo_core::scheduler::{self, ScheduleStatus};
use explo_core::state::{RunLock, RunRecord, State};
use explo_core::tools::{self, ToolStatus};
use explo_core::{config, logfile};
use std::collections::VecDeque;
use std::process::ExitCode;
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{mpsc, Arc};
use std::time::{Duration, Instant};

const MAX_LOG_LINES: usize = 2000;
const GREEN: Color32 = Color32::from_rgb(52, 168, 83);
const RED: Color32 = Color32::from_rgb(217, 83, 79);
const AMBER: Color32 = Color32::from_rgb(230, 150, 30);

pub fn run() -> ExitCode {
    let options = eframe::NativeOptions {
        viewport: egui::ViewportBuilder::default().with_inner_size([980.0, 700.0]).with_min_inner_size([780.0, 540.0]).with_title("Explo"),
        ..Default::default()
    };
    match eframe::run_native("Explo", options, Box::new(|cc| Ok(Box::new(App::new(cc))))) {
        Ok(()) => ExitCode::SUCCESS,
        Err(e) => {
            eprintln!("Couldn't open the window: {e}");
            ExitCode::FAILURE
        }
    }
}

enum Msg {
    Event(Event),
    /// A background job finished. `Some` when it was a pipeline run.
    JobDone(Option<RunRecord>),
    Tools(Vec<ToolStatus>),
    Conn(Vec<(String, Result<String, String>)>),
}

#[derive(PartialEq, Clone, Copy)]
enum Tab {
    Dashboard,
    Settings,
    Schedule,
    Log,
}

struct LogLine {
    time: String,
    level: Level,
    text: String,
}

struct Job {
    cancel: Arc<AtomicBool>,
    started: Instant,
}

struct App {
    cfg: Config,
    saved_json: String,
    dirty_since: Option<Instant>,
    save_error: Option<String>,
    tab: Tab,
    tx: mpsc::Sender<Msg>,
    rx: mpsc::Receiver<Msg>,
    egui_ctx: egui::Context,
    log: VecDeque<LogLine>,
    show_debug: bool,
    job: Option<Job>,
    stage: String,
    progress: (usize, usize),
    state: State,
    tools: Vec<ToolStatus>,
    checking_tools: bool,
    conn: Vec<(String, Result<String, String>)>,
    testing: bool,
    sched: ScheduleStatus,
    sched_msg: Option<(bool, String)>,
    filter_buf: String,
    blacklist_buf: String,
    show_secrets: bool,
}

impl App {
    fn new(cc: &eframe::CreationContext<'_>) -> Self {
        setup_style(&cc.egui_ctx);
        setup_fonts(&cc.egui_ctx);
        let (tx, rx) = mpsc::channel();
        let mut load_error = None;
        let cfg = Config::load().unwrap_or_else(|e| {
            load_error = Some(format!("{e:#}"));
            Config::default()
        });
        let mut app = App {
            saved_json: serde_json::to_string(&cfg).unwrap_or_default(),
            filter_buf: cfg.download.filter_words.join(", "),
            blacklist_buf: cfg.listenbrainz.artist_blacklist.join("\n"),
            cfg,
            dirty_since: None,
            save_error: None,
            tab: Tab::Dashboard,
            tx,
            rx,
            egui_ctx: cc.egui_ctx.clone(),
            log: VecDeque::new(),
            show_debug: false,
            job: None,
            stage: String::new(),
            progress: (0, 0),
            state: State::load(),
            tools: vec![],
            checking_tools: false,
            conn: vec![],
            testing: false,
            sched: scheduler::status(),
            sched_msg: None,
            show_secrets: false,
        };
        if let Some(e) = load_error {
            app.push_log(Level::Error, format!("Couldn't read the saved settings (using defaults): {e}"));
        }
        if app.cfg.problems().len() >= 4 {
            app.tab = Tab::Settings; // first launch
        }
        app.check_tools();
        app
    }

    // ---------- plumbing ----------

    fn push_log(&mut self, level: Level, text: String) {
        if self.log.len() >= MAX_LOG_LINES {
            self.log.pop_front();
        }
        self.log.push_back(LogLine { time: Local::now().format("%H:%M:%S").to_string(), level, text });
    }

    /// A context whose events flow into the UI (and the log file).
    fn job_ctx(&self) -> Ctx {
        let (tx, egui_ctx) = (self.tx.clone(), self.egui_ctx.clone());
        Ctx::new(move |e| {
            if let Event::Log(l, m) = &e {
                logfile::append(*l, m);
            }
            let _ = tx.send(Msg::Event(e));
            egui_ctx.request_repaint();
        })
    }

    fn drain(&mut self) {
        while let Ok(msg) = self.rx.try_recv() {
            match msg {
                Msg::Event(Event::Log(l, m)) => self.push_log(l, m),
                Msg::Event(Event::Stage(s)) => self.stage = s,
                Msg::Event(Event::Progress { done, total }) => self.progress = (done, total),
                Msg::JobDone(rec) => {
                    self.job = None;
                    self.stage.clear();
                    self.progress = (0, 0);
                    self.state = State::load();
                    if let Some(r) = rec {
                        let (level, text) = if r.ok { (Level::Info, "Run finished".to_string()) } else { (Level::Warn, "Run finished with problems — see above".to_string()) };
                        self.push_log(level, text);
                    }
                }
                Msg::Tools(t) => {
                    self.tools = t;
                    self.checking_tools = false;
                }
                Msg::Conn(c) => {
                    self.conn = c;
                    self.testing = false;
                }
            }
        }
    }

    fn autosave(&mut self, ctx: &egui::Context) {
        let json = serde_json::to_string(&self.cfg).unwrap_or_default();
        if json == self.saved_json {
            self.dirty_since = None;
            return;
        }
        match self.dirty_since {
            None => self.dirty_since = Some(Instant::now()),
            Some(t) if t.elapsed() > Duration::from_millis(700) => self.save_now(),
            _ => {}
        }
        ctx.request_repaint_after(Duration::from_millis(750));
    }

    fn save_now(&mut self) {
        self.saved_json = serde_json::to_string(&self.cfg).unwrap_or_default();
        self.dirty_since = None;
        self.save_error = self.cfg.save().err().map(|e| format!("{e:#}"));
    }

    fn spawn_job(&mut self, f: impl FnOnce(&Ctx) -> Option<RunRecord> + Send + 'static) {
        if self.job.is_some() {
            return;
        }
        let ctx = self.job_ctx();
        self.job = Some(Job { cancel: ctx.cancel_handle(), started: Instant::now() });
        self.stage = "Starting…".into();
        self.progress = (0, 0);
        let (tx, egui_ctx) = (self.tx.clone(), self.egui_ctx.clone());
        std::thread::spawn(move || {
            let rec = f(&ctx);
            let _ = tx.send(Msg::JobDone(rec));
            egui_ctx.request_repaint();
        });
    }

    fn start_run(&mut self, kinds: Vec<String>, trigger: Trigger) {
        let problems = self.cfg.problems();
        if !problems.is_empty() {
            self.push_log(Level::Error, format!("Can't run yet: {}", problems.join("; ")));
            self.tab = Tab::Settings;
            return;
        }
        self.save_now();
        let cfg = self.cfg.clone();
        self.spawn_job(move |ctx| match RunLock::try_acquire() {
            Ok(Some(_lock)) => {
                let _awake = KeepAwake::new();
                Some(pipeline::run_all(&cfg, &kinds, trigger, ctx))
            }
            Ok(None) => {
                ctx.warn("Another Explo run (probably the scheduled one) is already in progress");
                None
            }
            Err(e) => {
                ctx.error(format!("{e:#}"));
                None
            }
        });
    }

    fn check_tools(&mut self) {
        self.checking_tools = true;
        let (cfg, tx, c) = (self.cfg.download.clone(), self.tx.clone(), self.egui_ctx.clone());
        std::thread::spawn(move || {
            let _ = tx.send(Msg::Tools(tools::check(&cfg)));
            c.request_repaint();
        });
    }

    fn test_connections(&mut self) {
        self.testing = true;
        self.conn.clear();
        let (cfg, tx, c) = (self.cfg.clone(), self.tx.clone(), self.egui_ctx.clone());
        std::thread::spawn(move || {
            let res = pipeline::test_connections(&cfg).into_iter().map(|(n, r)| (n, r.map_err(|e| format!("{e:#}")))).collect();
            let _ = tx.send(Msg::Conn(res));
            c.request_repaint();
        });
    }

    // ---------- screens ----------

    fn sidebar(&mut self, ui: &mut egui::Ui) {
        ui.add_space(8.0);
        ui.label(RichText::new("Explo").size(26.0).strong());
        ui.label(RichText::new("weekly music discovery").weak());
        ui.add_space(18.0);
        for (tab, label) in [(Tab::Dashboard, "Dashboard"), (Tab::Settings, "Settings"), (Tab::Schedule, "Schedule"), (Tab::Log, "Log")] {
            if nav_item(ui, label, self.tab == tab).clicked() {
                self.tab = tab;
            }
        }
        ui.with_layout(egui::Layout::bottom_up(egui::Align::LEFT), |ui| {
            ui.add_space(6.0);
            if self.dirty_since.is_some() {
                ui.label(RichText::new("Saving…").weak().small());
            } else if let Some(e) = &self.save_error {
                ui.label(RichText::new(format!("Couldn't save: {e}")).color(RED).small());
            } else {
                ui.label(RichText::new("Settings saved").weak().small());
            }
            ui.add_space(6.0);
            if self.job.is_some() {
                ui.horizontal(|ui| {
                    ui.spinner();
                    ui.label("Running…");
                });
            } else {
                ui.horizontal(|ui| {
                    let (r, _) = ui.allocate_exact_size(egui::vec2(10.0, 10.0), egui::Sense::hover());
                    ui.painter().circle_filled(r.center(), 4.0, GREEN);
                    ui.label("Idle");
                });
            }
        });
    }

    fn dashboard(&mut self, ui: &mut egui::Ui) {
        ui.heading("Dashboard");
        ui.label(RichText::new("Fetches your ListenBrainz recommendations, downloads what your library is missing, and builds playlists on your music server.").weak());
        ui.add_space(12.0);

        let problems = self.cfg.problems();
        if !problems.is_empty() {
            let mut open_settings = false;
            card(ui, "Setup needed", |ui| {
                for p in &problems {
                    ui.label(format!("•  {p}"));
                }
                if ui.button("Open Settings").clicked() {
                    open_settings = true;
                }
            });
            if open_settings {
                self.tab = Tab::Settings;
            }
        }

        let running = self.job.is_some();
        let mut run_one: Option<String> = None;
        card(ui, "Playlists", |ui| {
            ui.label(RichText::new("Ticked playlists are included in “Run all” and in the weekly schedule.").weak().small());
            ui.add_space(4.0);
            for (id, label) in PLAYLIST_KINDS {
                ui.horizontal(|ui| {
                    let mut on = self.cfg.playlist_enabled(id);
                    if ui.checkbox(&mut on, label).changed() {
                        self.cfg.set_playlist_enabled(id, on);
                    }
                    ui.with_layout(egui::Layout::right_to_left(egui::Align::Center), |ui| {
                        if ui.add_enabled(!running, egui::Button::new("Run now")).clicked() {
                            run_one = Some(id.to_string());
                        }
                    });
                });
            }
        });
        if let Some(id) = run_one {
            self.start_run(vec![id], Trigger::ManualOne);
        }

        let mut run_all = false;
        let mut cancel = false;
        card(ui, "Run", |ui| {
            if let Some(job) = &self.job {
                ui.label(RichText::new(&self.stage).strong());
                let (done, total) = self.progress;
                let bar = if total > 0 { egui::ProgressBar::new(done as f32 / total as f32).text(format!("{done} / {total}")) } else { egui::ProgressBar::new(0.0).animate(true) };
                ui.add(bar);
                ui.horizontal(|ui| {
                    ui.label(RichText::new(format!("{}s elapsed", job.started.elapsed().as_secs())).weak());
                    if ui.button("Cancel").clicked() {
                        cancel = true;
                    }
                });
            } else {
                let n = self.cfg.playlists.enabled.len();
                let btn = egui::Button::new(RichText::new(format!("▶  Run all enabled playlists ({n})")).size(16.0)).min_size(egui::vec2(0.0, 38.0));
                if ui.add_enabled(n > 0, btn).clicked() {
                    run_all = true;
                }
            }
        });
        if cancel {
            if let Some(j) = &self.job {
                j.cancel.store(true, Ordering::Relaxed);
                self.stage = "Cancelling…".into();
            }
        }
        if run_all {
            self.start_run(self.cfg.playlists.enabled.clone(), Trigger::ManualAll);
        }

        card(ui, "Last run", |ui| match self.state.history.first() {
            None => {
                ui.label("Nothing has run yet.");
            }
            Some(r) => {
                ui.horizontal(|ui| {
                    ui.label(r.finished.with_timezone(&Local).format("%a %d %b, %H:%M").to_string());
                    ui.label(RichText::new(if r.ok { "✔ succeeded" } else { "✖ problems" }).color(if r.ok { GREEN } else { RED }));
                    ui.label(RichText::new(format!("({})", r.trigger)).weak());
                });
                for line in r.summary().lines() {
                    ui.label(RichText::new(line).small());
                }
            }
        });

        card(ui, "Next scheduled run", |ui| {
            if self.sched.installed {
                if let Some(n) = schedule::next_run(Local::now(), &self.cfg.schedule) {
                    ui.label(format!("{} — a Mac that's asleep then runs it as soon as it wakes.", n.format("%A %d %B, %H:%M")));
                }
            } else {
                ui.label("The weekly schedule isn't installed.");
                if ui.button("Set up…").clicked() {
                    self.tab = Tab::Schedule;
                }
            }
        });

        let mut recheck = false;
        card(ui, "Required tools", |ui| {
            if self.checking_tools {
                ui.label("Checking…");
            }
            for t in &self.tools {
                ui.horizontal(|ui| match &t.path {
                    Some(p) => {
                        ui.label(RichText::new("✔").color(GREEN));
                        ui.label(RichText::new(t.name).strong());
                        ui.label(RichText::new(format!("{}  {}", t.version.clone().unwrap_or_default(), p.display())).weak().small());
                    }
                    None => {
                        ui.label(RichText::new("✖").color(RED));
                        ui.label(RichText::new(t.name).strong());
                        ui.label(RichText::new("not found").color(RED));
                    }
                });
            }
            if self.tools.iter().any(|t| t.path.is_none() && t.name != "ffprobe") {
                ui.label(RichText::new("Install them with Homebrew:  brew install yt-dlp ffmpeg").monospace());
            }
            if ui.button("Re-check").clicked() {
                recheck = true;
            }
        });
        if recheck {
            self.check_tools();
        }
    }

    fn settings(&mut self, ui: &mut egui::Ui) {
        ui.heading("Settings");
        ui.label(RichText::new("Changes are saved automatically.").weak());
        ui.checkbox(&mut self.show_secrets, "Show passwords and tokens");
        ui.add_space(8.0);
        let secret = !self.show_secrets;

        card(ui, "ListenBrainz", |ui| {
            egui::Grid::new("lb").num_columns(2).spacing([14.0, 8.0]).show(ui, |ui| {
                ui.label("Username");
                ui.add(egui::TextEdit::singleline(&mut self.cfg.listenbrainz.user).desired_width(320.0));
                ui.end_row();
                ui.label("User token");
                ui.add(egui::TextEdit::singleline(&mut self.cfg.listenbrainz.token).password(secret).desired_width(320.0).hint_text("optional — listenbrainz.org/settings"));
                ui.end_row();
                ui.label("Featured artists");
                ui.checkbox(&mut self.cfg.listenbrainz.single_artist, "Put them in the title: “Song (feat. X)”");
                ui.end_row();
                ui.label("Skip artists");
                if ui.add(egui::TextEdit::multiline(&mut self.blacklist_buf).desired_rows(2).desired_width(320.0).hint_text("one name or MusicBrainz ID per line")).changed() {
                    self.cfg.listenbrainz.artist_blacklist = split_list(&self.blacklist_buf, true);
                }
                ui.end_row();
            });
        });

        card(ui, "Music server (Navidrome / Subsonic)", |ui| {
            egui::Grid::new("srv").num_columns(2).spacing([14.0, 8.0]).show(ui, |ui| {
                ui.label("Address");
                ui.add(egui::TextEdit::singleline(&mut self.cfg.server.url).desired_width(320.0).hint_text("http://127.0.0.1:4533"));
                ui.end_row();
                ui.label("Username");
                ui.add(egui::TextEdit::singleline(&mut self.cfg.server.user).desired_width(320.0));
                ui.end_row();
                ui.label("Password");
                ui.add(egui::TextEdit::singleline(&mut self.cfg.server.password).password(secret).desired_width(320.0));
                ui.end_row();
                ui.label("Playlists");
                ui.checkbox(&mut self.cfg.server.public_playlist, "Make them public");
                ui.end_row();
                ui.label("Match strictness");
                ui.add(egui::Slider::new(&mut self.cfg.server.match_score, 30..=100));
                ui.end_row();
            });
            egui::CollapsingHeader::new("Advanced").id_salt("srv-adv").show(ui, |ui| {
                ui.label(RichText::new("Some servers only let an admin trigger a library scan.").weak().small());
                egui::Grid::new("srv2").num_columns(2).spacing([14.0, 8.0]).show(ui, |ui| {
                    ui.label("Admin username");
                    ui.add(egui::TextEdit::singleline(&mut self.cfg.server.admin_user).desired_width(320.0));
                    ui.end_row();
                    ui.label("Admin password");
                    ui.add(egui::TextEdit::singleline(&mut self.cfg.server.admin_password).password(secret).desired_width(320.0));
                    ui.end_row();
                    ui.label("Scan timeout (min)");
                    ui.add(egui::DragValue::new(&mut self.cfg.server.scan_timeout_minutes).range(1..=120));
                    ui.end_row();
                });
            });
            ui.add_space(6.0);
            ui.horizontal(|ui| {
                if ui.add_enabled(!self.testing, egui::Button::new("Test connections")).clicked() {
                    self.test_connections();
                }
                if self.testing {
                    ui.spinner();
                }
            });
            for (name, res) in &self.conn {
                match res {
                    Ok(m) => ui.label(RichText::new(format!("✔ {name}: {m}")).color(GREEN)),
                    Err(e) => ui.label(RichText::new(format!("✖ {name}: {e}")).color(RED)),
                };
            }
        });

        card(ui, "Downloads", |ui| {
            egui::Grid::new("dl").num_columns(2).spacing([14.0, 8.0]).show(ui, |ui| {
                ui.label("Folder");
                ui.horizontal(|ui| {
                    ui.add(egui::TextEdit::singleline(&mut self.cfg.download.dir).desired_width(320.0));
                    #[cfg(any(target_os = "macos", target_os = "windows"))]
                    if ui.button("Browse…").clicked() {
                        if let Some(p) = rfd::FileDialog::new().pick_folder() {
                            self.cfg.download.dir = p.to_string_lossy().into_owned();
                        }
                    }
                });
                ui.end_row();
                ui.label("");
                ui.label(RichText::new("Must be inside (or shared with) your server's music library.").weak().small());
                ui.end_row();
                ui.label("Sub-folders");
                ui.checkbox(&mut self.cfg.download.use_subdirectory, "One folder per playlist");
                ui.end_row();
                ui.label("Format");
                egui::ComboBox::from_id_salt("ext").selected_text(self.cfg.download.extension.clone()).show_ui(ui, |ui| {
                    for e in ["mp3", "m4a", "opus", "ogg", "flac"] {
                        ui.selectable_value(&mut self.cfg.download.extension, e.to_string(), e);
                    }
                });
                ui.end_row();
                ui.label("Cover art");
                ui.checkbox(&mut self.cfg.download.embed_cover_art, "Embed (mp3, m4a, flac)");
                ui.end_row();
                ui.label("Simultaneous downloads");
                ui.add(egui::Slider::new(&mut self.cfg.download.parallel, 1..=6));
                ui.end_row();
                ui.label("Max new tracks per run");
                ui.add(egui::DragValue::new(&mut self.cfg.download.max_downloads_per_run).range(0..=500).custom_formatter(|n, _| if n == 0.0 { "no limit".to_string() } else { format!("{n}") }));
                ui.end_row();
                ui.label("Skip results containing");
                if ui.add(egui::TextEdit::singleline(&mut self.filter_buf).desired_width(320.0).hint_text("live, remix, instrumental")).changed() {
                    self.cfg.download.filter_words = split_list(&self.filter_buf, false);
                }
                ui.end_row();
            });
            egui::CollapsingHeader::new("Advanced").id_salt("dl-adv").show(ui, |ui| {
                egui::Grid::new("dl2").num_columns(2).spacing([14.0, 8.0]).show(ui, |ui| {
                    ui.label("yt-dlp path");
                    ui.add(egui::TextEdit::singleline(&mut self.cfg.download.ytdlp_path).desired_width(320.0).hint_text("auto-detect"));
                    ui.end_row();
                    ui.label("ffmpeg path");
                    ui.add(egui::TextEdit::singleline(&mut self.cfg.download.ffmpeg_path).desired_width(320.0).hint_text("auto-detect"));
                    ui.end_row();
                    ui.label("Cookies file");
                    ui.add(egui::TextEdit::singleline(&mut self.cfg.download.cookies_path).desired_width(320.0).hint_text("optional, for yt-dlp"));
                    ui.end_row();
                });
            });
        });

        card(ui, "Playlist names", |ui| {
            ui.checkbox(&mut self.cfg.playlists.replace, "Replace last week's playlist instead of keeping one per week");
            ui.add_enabled_ui(!self.cfg.playlists.replace, |ui| {
                ui.horizontal(|ui| {
                    ui.radio_value(&mut self.cfg.playlists.name_format, NameFormat::Week, "Name-2026-Week48");
                    ui.radio_value(&mut self.cfg.playlists.name_format, NameFormat::Date, "Name-2026-11-30");
                });
            });
            ui.add_space(4.0);
            egui::Grid::new("names").num_columns(2).spacing([14.0, 8.0]).show(ui, |ui| {
                for (id, _) in PLAYLIST_KINDS {
                    ui.label(id);
                    let entry = self.cfg.playlists.custom_names.entry(id.to_string()).or_default();
                    ui.add(egui::TextEdit::singleline(entry).desired_width(320.0).hint_text(explo_core::util::title_case_kind(id)));
                    ui.end_row();
                }
            });
            self.cfg.playlists.custom_names.retain(|_, v| !v.trim().is_empty());
        });

        card(ui, "Notifications", |ui| {
            ui.checkbox(&mut self.cfg.notify, "Show a notification when a scheduled run finishes");
        });
    }

    fn schedule_tab(&mut self, ui: &mut egui::Ui) {
        ui.heading("Weekly schedule");
        ui.label(RichText::new("Installs a background job with the system scheduler. Docker or a running server isn't needed.").weak());
        ui.add_space(10.0);

        if !self.sched.supported {
            card(ui, "Not available", |ui| {
                ui.label(&self.sched.detail);
                ui.label("You can still run everything from the Dashboard.");
            });
            return;
        }

        card(ui, "When", |ui| {
            ui.horizontal(|ui| {
                ui.label("Every");
                egui::ComboBox::from_id_salt("weekday").selected_text(WEEKDAYS[(self.cfg.schedule.weekday.clamp(1, 7) - 1) as usize]).show_ui(ui, |ui| {
                    for (i, d) in WEEKDAYS.iter().enumerate() {
                        ui.selectable_value(&mut self.cfg.schedule.weekday, (i + 1) as u8, *d);
                    }
                });
                ui.label("at");
                ui.add(egui::DragValue::new(&mut self.cfg.schedule.hour).range(0..=23).custom_formatter(|n, _| format!("{:02}", n as u32)));
                ui.label(":");
                ui.add(egui::DragValue::new(&mut self.cfg.schedule.minute).range(0..=59).custom_formatter(|n, _| format!("{:02}", n as u32)));
            });
            ui.label(RichText::new("ListenBrainz usually publishes the new weekly playlists early on Monday, so mid-morning is a safe choice.").weak().small());
            if let Some(n) = schedule::next_run(Local::now(), &self.cfg.schedule) {
                ui.label(format!("Next: {}", n.format("%A %d %B, %H:%M")));
            }
        });

        let exe = std::env::current_exe().ok();
        let warning = exe.as_ref().and_then(|e| exe_warning(e));
        let (mut install, mut uninstall, mut due_check) = (false, false, false);
        card(ui, "Status", |ui| {
            let (txt, color) = match (self.sched.installed, self.sched.loaded) {
                (true, true) => ("✔ Installed and active", GREEN),
                (true, false) => ("⚠ Installed but not loaded — press Install / Update", AMBER),
                _ => ("Not installed", Color32::GRAY),
            };
            ui.label(RichText::new(txt).color(color).strong());
            if let Some(job_exe) = &self.sched.exe {
                ui.label(RichText::new(format!("Runs: {job_exe}")).weak().small());
                if exe.as_ref().is_some_and(|e| e.to_string_lossy() != job_exe.as_str()) {
                    ui.label(RichText::new("The scheduled job points to a different copy of Explo than this one. Press Install / Update to point it here.").color(AMBER).small());
                }
            }
            if let Some(w) = &warning {
                ui.label(RichText::new(w).color(AMBER));
            }
            ui.add_space(6.0);
            ui.horizontal(|ui| {
                if ui.add_enabled(self.job.is_none() && warning.as_ref().is_none_or(|w| !w.contains("translocat")), egui::Button::new(if self.sched.installed { "Install / Update" } else { "Install weekly schedule" })).clicked() {
                    install = true;
                }
                if ui.add_enabled(self.sched.installed, egui::Button::new("Uninstall")).clicked() {
                    uninstall = true;
                }
                if ui.add_enabled(self.job.is_none() && self.sched.installed, egui::Button::new("Check now: is a run due?")).clicked() {
                    due_check = true;
                }
            });
            if let Some((ok, msg)) = &self.sched_msg {
                ui.label(RichText::new(msg).color(if *ok { GREEN } else { RED }));
            }
        });

        card(ui, "How it behaves", |ui| {
            for line in [
                "Asleep at the scheduled time: the job runs right after the Mac wakes (launchd, unlike cron, doesn't skip it).",
                "Powered off or logged out: it catches up at the next login, and an hourly check covers anything else.",
                "Network not up yet after waking: it waits (up to 5 minutes) before starting.",
                "A failed run is retried hourly, up to 6 times per week. A successful one isn't repeated.",
                "The Mac is kept awake while a run is in progress.",
                "If your music folder is on an external or network drive, macOS may ask for permission the first time the background job touches it.",
            ] {
                ui.label(format!("•  {line}"));
            }
        });

        if install {
            self.save_now();
            self.sched_msg = Some(match exe.as_ref().map(|e| scheduler::install(&self.cfg.schedule, e)) {
                Some(Ok(())) => (true, "Installed. The first check happens right away; runs only start when due.".to_string()),
                Some(Err(e)) => (false, format!("{e:#}")),
                None => (false, "Couldn't determine this app's location".to_string()),
            });
            self.sched = scheduler::status();
        }
        if uninstall {
            self.sched_msg = Some(match scheduler::uninstall() {
                Ok(()) => (true, "Schedule removed.".to_string()),
                Err(e) => (false, format!("{e:#}")),
            });
            self.sched = scheduler::status();
        }
        if due_check {
            self.save_now();
            self.spawn_job(|ctx| {
                match pipeline::scheduled(ctx, false) {
                    Ok(_) => {}
                    Err(e) => ctx.error(format!("{e:#}")),
                }
                None
            });
            self.tab = Tab::Log;
        }
    }

    fn log_tab(&mut self, ui: &mut egui::Ui) {
        ui.horizontal(|ui| {
            ui.heading("Log");
            ui.with_layout(egui::Layout::right_to_left(egui::Align::Center), |ui| {
                if ui.button("Open log folder").clicked() {
                    platform::open(&config::log_dir());
                }
                if ui.button("Open log file").clicked() {
                    platform::open(&logfile::path());
                }
                if ui.button("Clear").clicked() {
                    self.log.clear();
                }
                ui.checkbox(&mut self.show_debug, "Details");
            });
        });
        ui.add_space(4.0);
        egui::ScrollArea::vertical().auto_shrink([false, false]).stick_to_bottom(true).show(ui, |ui| {
            if self.log.is_empty() {
                ui.label(RichText::new("Nothing yet. Scheduled runs are written to the log file (use “Open log file”).").weak());
            }
            for l in self.log.iter().filter(|l| self.show_debug || l.level >= Level::Info) {
                let color = match l.level {
                    Level::Error => RED,
                    Level::Warn => AMBER,
                    Level::Debug => Color32::GRAY,
                    Level::Info => ui.visuals().text_color(),
                };
                ui.label(RichText::new(format!("{}  {}", l.time, l.text)).monospace().size(12.5).color(color));
            }
        });
    }
}

impl eframe::App for App {
    fn update(&mut self, ctx: &egui::Context, _frame: &mut eframe::Frame) {
        self.drain();
        self.autosave(ctx);
        if self.job.is_some() {
            ctx.request_repaint_after(Duration::from_millis(500)); // elapsed-time counter
        }
        egui::SidePanel::left("nav").exact_width(180.0).resizable(false).show(ctx, |ui| self.sidebar(ui));
        egui::CentralPanel::default().show(ctx, |ui| {
            if self.tab == Tab::Log {
                self.log_tab(ui);
            } else {
                egui::ScrollArea::vertical().id_salt(self.tab as u8).auto_shrink([false, false]).show(ui, |ui| match self.tab {
                    Tab::Dashboard => self.dashboard(ui),
                    Tab::Settings => self.settings(ui),
                    Tab::Schedule => self.schedule_tab(ui),
                    Tab::Log => {}
                });
            }
        });
    }

    fn on_exit(&mut self, _gl: Option<&eframe::glow::Context>) {
        if let Some(j) = &self.job {
            j.cancel.store(true, Ordering::Relaxed);
        }
        self.save_now();
    }
}

// ---------- helpers ----------

/// A full-width, left-aligned sidebar entry.
fn nav_item(ui: &mut egui::Ui, label: &str, selected: bool) -> egui::Response {
    let (rect, resp) = ui.allocate_exact_size(egui::vec2(ui.available_width(), 34.0), egui::Sense::click());
    let visuals = ui.visuals();
    let fill = if selected {
        visuals.selection.bg_fill
    } else if resp.hovered() {
        visuals.widgets.hovered.weak_bg_fill
    } else {
        Color32::TRANSPARENT
    };
    let text_color = if selected { visuals.selection.stroke.color } else { visuals.text_color() };
    ui.painter().rect_filled(rect, egui::CornerRadius::same(8), fill);
    ui.painter().text(rect.left_center() + egui::vec2(14.0, 0.0), egui::Align2::LEFT_CENTER, label, egui::FontId::proportional(15.5), text_color);
    resp.on_hover_cursor(egui::CursorIcon::PointingHand)
}

fn card(ui: &mut egui::Ui, title: &str, add: impl FnOnce(&mut egui::Ui)) {
    egui::Frame::new()
        .fill(ui.visuals().faint_bg_color)
        .stroke(ui.visuals().widgets.noninteractive.bg_stroke)
        .corner_radius(egui::CornerRadius::same(10))
        .inner_margin(egui::Margin::same(14))
        .show(ui, |ui| {
            ui.set_width(ui.available_width());
            ui.label(RichText::new(title).strong().size(15.5));
            ui.add_space(6.0);
            add(ui);
        });
    ui.add_space(10.0);
}

fn split_list(s: &str, newlines_only: bool) -> Vec<String> {
    s.split(|c| c == '\n' || (!newlines_only && c == ',')).map(|p| p.trim().to_string()).filter(|p| !p.is_empty()).collect()
}

/// Paths a LaunchAgent must not point at.
fn exe_warning(exe: &std::path::Path) -> Option<String> {
    let p = exe.to_string_lossy();
    if p.contains("AppTranslocation") {
        Some("macOS is running Explo from a temporary location (app translocation). Move Explo.app to /Applications, open it from there, then install the schedule — otherwise the job would break.".to_string())
    } else if p.contains("/target/debug/") || p.contains("/target/release/") {
        Some("Running from a build folder: the schedule will point here, so reinstall it if you move or rebuild the binary.".to_string())
    } else {
        None
    }
}

fn setup_style(ctx: &egui::Context) {
    ctx.style_mut(|s| {
        s.spacing.item_spacing = egui::vec2(10.0, 8.0);
        s.spacing.button_padding = egui::vec2(14.0, 6.0);
        s.spacing.interact_size.y = 26.0;
        let r = egui::CornerRadius::same(8);
        s.visuals.widgets.noninteractive.corner_radius = r;
        s.visuals.widgets.inactive.corner_radius = r;
        s.visuals.widgets.hovered.corner_radius = r;
        s.visuals.widgets.active.corner_radius = r;
        s.visuals.widgets.open.corner_radius = r;
    });
}

/// egui's built-in fonts only cover Latin/emoji; borrow macOS system fonts so track titles in
/// other scripts show up as letters instead of boxes. (egui doesn't do complex-script shaping.)
fn setup_fonts(ctx: &egui::Context) {
    let candidates = [
        "/System/Library/Fonts/Supplemental/Arial Unicode.ttf",
        "/System/Library/Fonts/Supplemental/Tamil Sangam MN.ttc",
        "/System/Library/Fonts/Supplemental/Devanagari Sangam MN.ttc",
        "/System/Library/Fonts/PingFang.ttc",
        "/System/Library/Fonts/AppleSDGothicNeo.ttc",
    ];
    let mut fonts = egui::FontDefinitions::default();
    for (i, path) in candidates.iter().enumerate() {
        if let Ok(bytes) = std::fs::read(path) {
            let name = format!("system-{i}");
            fonts.font_data.insert(name.clone(), Arc::new(egui::FontData::from_owned(bytes)));
            for family in [egui::FontFamily::Proportional, egui::FontFamily::Monospace] {
                fonts.families.entry(family).or_default().push(name.clone());
            }
        }
    }
    ctx.set_fonts(fonts);
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn splits_lists() {
        assert_eq!(split_list("live, remix ,, instrumental", false), vec!["live", "remix", "instrumental"]);
        assert_eq!(split_list("Name, With Comma\nOther", true), vec!["Name, With Comma", "Other"]);
    }

    #[test]
    fn warns_about_translocated_apps() {
        assert!(exe_warning(std::path::Path::new("/private/var/folders/x/AppTranslocation/ABC/d/Explo.app/Contents/MacOS/explo")).is_some());
        assert!(exe_warning(std::path::Path::new("/Applications/Explo.app/Contents/MacOS/explo")).is_none());
    }
}
