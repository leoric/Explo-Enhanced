package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ilyakaznacheev/cleanenv"
)

func TestGetPlaylistName(t *testing.T) {
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.Local) // ISO week 48, day 330

	cases := []struct {
		name, playlist, base, format string
		replace                      bool
		want                         string
	}{
		{"default replace", "weekly-exploration", "", "week", true, "Weekly-Exploration"},
		{"custom replace", "weekly-exploration", "Discover Weekly", "week", true, "Discover Weekly"},
		{"default week", "weekly-exploration", "", "week", false, "Weekly-Exploration-2026-Week48"},
		{"custom week", "weekly-exploration", "Discover Weekly", "week", false, "Discover Weekly-2026-Week48"},
		{"default date", "weekly-jams", "", "date", false, "Weekly-Jams-2026-11-26"},
		{"custom date", "weekly-jams", "Jams", "date", false, "Jams-2026-11-26"},
		{"daily-jams default", "daily-jams", "", "week", false, "Daily-Jams-2026-Day330"},
		{"daily-jams custom", "daily-jams", "Täglich", "week", false, "Täglich-2026-Day330"},
		{"custom-* playlist", "custom-abc123", "", "week", false, "Custom-Abc123"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := getPlaylistName(tc.playlist, tc.base, tc.format, tc.replace, now); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestPlaylistNamesForType(t *testing.T) {
	n := PlaylistNames{WeeklyExploration: " Discover Weekly ", FreshReleases: "New"}
	if got := n.ForType("weekly-exploration"); got != "Discover Weekly" {
		t.Fatalf("got %q", got)
	}
	if got := n.ForType("fresh-releases"); got != "New" {
		t.Fatalf("got %q", got)
	}
	if got := n.ForType("weekly-jams"); got != "" {
		t.Fatalf("got %q", got)
	}
	if got := n.ForType("custom-abc"); got != "" {
		t.Fatalf("got %q", got)
	}
}

func TestGenPlaylistDetailsKeepsSubfolder(t *testing.T) {
	var cfg Config
	cfg.Flags.Playlist = "weekly-exploration"
	cfg.ReplacePlaylist = true
	cfg.ClientCfg.PlaylistNames.WeeklyExploration = "Discover Weekly"
	cfg.DownloadCfg.DownloadDir = "/data/"
	cfg.DownloadCfg.UseSubDir = true

	cfg.GenPlaylistDetails()

	if cfg.ClientCfg.PlaylistName != "Discover Weekly" {
		t.Fatalf("PlaylistName = %q", cfg.ClientCfg.PlaylistName)
	}
	if cfg.DownloadCfg.DownloadDir != "/data/Weekly-Exploration" {
		t.Fatalf("DownloadDir = %q", cfg.DownloadCfg.DownloadDir)
	}
	if cfg.DownloadCfg.RootDir != "/data/" {
		t.Fatalf("RootDir = %q", cfg.DownloadCfg.RootDir)
	}
}

func TestPlaylistNamesFromEnvFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte("WEEKLY_EXPLORATION_NAME=Discover Weekly\nDAILY_JAMS_NAME='Daily Mix'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var cfg Config
	if err := cleanenv.ReadConfig(path, &cfg); err != nil {
		t.Fatal(err)
	}
	if got := cfg.ClientCfg.PlaylistNames.WeeklyExploration; got != "Discover Weekly" {
		t.Fatalf("WeeklyExploration = %q", got)
	}
	if got := cfg.ClientCfg.PlaylistNames.DailyJams; got != "Daily Mix" {
		t.Fatalf("DailyJams = %q", got)
	}
}
