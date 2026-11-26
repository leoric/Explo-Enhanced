package downloader

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	cfg "explo/src/config"
	"explo/src/models"
	"explo/src/util"

	ffmpeg "github.com/u2takey/ffmpeg-go"

	"golang.org/x/sync/errgroup"
	"golang.org/x/time/rate"
)

type DownloadClient struct {
	Cfg         *cfg.DownloadConfig
	Downloaders []Downloader
}

type Downloader interface {
	QueryTrack(*models.Track) error
	GetTrack(*models.Track) error
	Monitor
}

// get download services from config and append them to DownloadClient
func NewDownloader(cfg *cfg.DownloadConfig, httpClient *util.HttpClient, filterLocal bool) (*DownloadClient, error) {
	var downloader []Downloader
	for _, service := range cfg.Services {
		switch service {
		case "youtube":
			downloader = append(downloader, NewYoutube(cfg.Youtube, cfg.Discovery, cfg.DownloadDir, httpClient))
		case "slskd":
			slskdClient := NewSlskd(cfg.Slskd, cfg.DownloadDir)
			slskdClient.AddHeader()
			downloader = append(downloader, slskdClient)
		case "lidarr":
			lidarrClient := NewLidarr(cfg.Lidarr, cfg.DownloadDir)
			lidarrClient.AddHeader()
			if err := lidarrClient.getRootDirectory(); err != nil {
				return nil, err
			}
			downloader = append(downloader, lidarrClient)
		default:
			return nil, fmt.Errorf("downloader '%s' not supported", service)
		}
	}

	return &DownloadClient{
		Cfg:         cfg,
		Downloaders: downloader}, nil
}

func (c *DownloadClient) StartDownload(tracks *[]*models.Track) {
	if c.Cfg.ExcludeLocal { // remove locally found tracks, so they can't be added to playlist
		filterLocalTracks(tracks, true)
	}

	if c.needsDownloadDir() {
		if err := os.MkdirAll(c.Cfg.DownloadDir, 0755); err != nil {
			slog.Error(err.Error())
			return
		}
	}

	for _, d := range c.Downloaders {
		var g errgroup.Group
		g.SetLimit(3)

		limiter := rate.NewLimiter(rate.Every(time.Second), c.Cfg.DownloadLimiter)

		for _, track := range *tracks {
			if track.Present {
				continue
			}

			track := track

			g.Go(func() error {
				ctx := context.Background()

				if err := limiter.Wait(ctx); err != nil {
					return err
				}

				if err := d.QueryTrack(track); err != nil {
					slog.Warn(err.Error())
					return nil
				}

				if err := limiter.Wait(ctx); err != nil {
					return err
				}

				if err := d.GetTrack(track); err != nil {
					slog.Warn(err.Error())
					return nil
				}

				return nil
			})
		}

		if err := g.Wait(); err != nil {
			slog.Warn(err.Error())
			return
		}

		if m, ok := d.(Monitor); ok {
			if err := c.MonitorDownloads(*tracks, m); err != nil {
				slog.Warn(err.Error())
			}
		}
	}

	filterLocalTracks(tracks, false)
}
func (c *DownloadClient) needsDownloadDir() bool {
	for _, svc := range c.Cfg.Services {
		if svc == "youtube" || svc == "youtube-music" {
			return true
		}
	}
	if c.Cfg.Lidarr.MigrateDL {
		return c.Cfg.Lidarr.MigrateDL
	}
	return c.Cfg.Slskd.MigrateDL
}

// audio extensions deleted by DeleteSongs, in addition to the configured slskd/youtube ones
var cleanupExtensions = []string{"flac", "mp3", "m4a", "opus", "ogg", "wav", "aac"}

// DeleteSongs removes the previous run's tracks from the playlist download directory, as decided by rules:
// keepers are moved to KEEPERS_DIR (keeping their path below the playlist directory), and the current run's
// tracks are left in place
func (c *DownloadClient) DeleteSongs(rules *CleanupRules) {
	exts := append(slices.Clone(cleanupExtensions), c.Cfg.Slskd.Filters.Extensions...)
	exts = append(exts, c.Cfg.Youtube.FileExtension)

	plan := &cleanupPlan{action: rules.action}
	if rules.keepers != nil {
		plan.keepersDir = c.Cfg.KeepersDir
	}
	res, err := cleanPlaylistDir(c.Cfg.RootDir, c.Cfg.DownloadDir, exts, plan)
	if err != nil {
		slog.Error("failed to clean download directory", "dir", c.Cfg.DownloadDir, "context", err.Error())
		return
	}
	slog.Info("cleaned download directory", "dir", c.Cfg.DownloadDir, "deleted files", res.deleted, "moved keepers", res.moved,
		"left current tracks", res.left, "removed folders", res.removedDirs, "kept files", res.kept)
}

// cleanupPlan decides per audio file whether cleanup deletes, moves or leaves it
type cleanupPlan struct {
	keepersDir string // target of actionMove, required for moving
	action     func(path string) (fileAction, error)
}

type cleanResult struct {
	deleted, moved, left, removedDirs, kept int
}

// cleanPlaylistDir recursively deletes audio files in dir, then removes directories left empty
// (keeping dir itself). Symlinks and non-audio files are kept. dir must be a strict subdirectory of root.
// With a plan, its action decides per audio file: moved files keep their path relative to dir below
// plan.keepersDir, files it can't decide on are left in place. Without one, every audio file is deleted.
func cleanPlaylistDir(root, dir string, exts []string, plan *cleanupPlan) (res cleanResult, err error) {
	dir, err = checkCleanTarget(root, dir)
	if err != nil {
		return res, err
	}
	keepersDir := ""
	if plan != nil && plan.keepersDir != "" {
		if keepersDir, err = checkKeepersDir(dir, plan.keepersDir); err != nil {
			return res, err
		}
	}

	audioExts := make(map[string]struct{}, len(exts))
	for _, ext := range exts {
		if ext = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(ext), ".")); ext != "" {
			audioExts[ext] = struct{}{}
		}
	}

	var dirs []string
	walkErr := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == dir {
				return err
			}
			slog.Warn("cannot access path during cleanup", "path", p, "context", err.Error())
			return nil
		}

		ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(p), "."))
		_, isAudio := audioExts[ext]

		switch {
		case d.IsDir():
			if p != dir {
				dirs = append(dirs, p)
			}
		case d.Type()&fs.ModeSymlink != 0:
			slog.Debug("keeping symlink", "path", p)
			res.kept++
		case !d.Type().IsRegular() || !isAudio:
			slog.Debug("keeping non-audio file", "path", p)
			res.kept++
		default:
			if plan != nil {
				action, err := plan.action(p)
				if err != nil {
					slog.Warn("could not read track tags, leaving it in place", "path", p, "context", err.Error())
					res.kept++
					return nil
				}
				switch action {
				case actionLeave:
					slog.Debug("leaving track of the current run in place", "path", p)
					res.left++
					return nil
				case actionMove:
					if keepersDir != "" && moveKeeper(dir, keepersDir, p) {
						res.moved++
					} else {
						res.kept++
					}
					return nil
				}
			}
			if err := os.Remove(p); err != nil {
				slog.Error("failed to remove file", "path", p, "context", err.Error())
				res.kept++
			} else {
				res.deleted++
			}
		}
		return nil
	})
	if walkErr != nil {
		if errors.Is(walkErr, fs.ErrNotExist) {
			return cleanResult{}, nil // nothing downloaded yet
		}
		return res, walkErr
	}

	// WalkDir visits parents before children, so reverse order removes the deepest directories first
	for i := len(dirs) - 1; i >= 0; i-- {
		if err := os.Remove(dirs[i]); err != nil {
			slog.Debug("keeping directory", "path", dirs[i], "context", err.Error())
			continue
		}
		res.removedDirs++
	}
	return res, nil
}

// moveKeeper moves file from below dir to the same relative path below keepersDir.
// An existing file at the target is never overwritten, the keeper then stays in place.
func moveKeeper(dir, keepersDir, file string) bool {
	rel, err := filepath.Rel(dir, file)
	if err != nil {
		slog.Warn("could not move keeper, leaving it in place", "path", file, "context", err.Error())
		return false
	}
	target := filepath.Join(keepersDir, rel)
	if _, err := os.Lstat(target); err == nil {
		slog.Warn("keeper already exists in KEEPERS_DIR, leaving it in place", "path", file, "target", target)
		return false
	}
	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		slog.Warn("could not move keeper, leaving it in place", "path", file, "context", err.Error())
		return false
	}
	if err := os.Rename(file, target); err != nil {
		// KEEPERS_DIR on another filesystem: copy, then remove the original
		if err := moveFile(file, target, true); err != nil {
			if _, statErr := os.Stat(file); statErr == nil {
				_ = os.Remove(target) // drop a partial copy, the original is still in place
			}
			slog.Warn("could not move keeper, leaving it in place", "path", file, "target", target, "context", err.Error())
			return false
		}
	}
	slog.Info("moved keeper", "path", file, "target", target)
	return true
}

// checkKeepersDir returns the absolute path of keepersDir, which must be outside the cleaned dir
func checkKeepersDir(dir, keepersDir string) (string, error) {
	abs, err := filepath.Abs(keepersDir)
	if err != nil {
		return "", err
	}
	if rel, err := filepath.Rel(dir, abs); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("refusing to clean %s: KEEPERS_DIR %s is inside it", dir, abs)
	}
	return abs, nil
}

// checkCleanTarget returns the absolute path of dir if it is a strict subdirectory of root
func checkCleanTarget(root, dir string) (string, error) {
	if strings.TrimSpace(root) == "" || strings.TrimSpace(dir) == "" {
		return "", fmt.Errorf("download directory is not set")
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(absRoot, absDir)
	if err != nil || absDir == string(filepath.Separator) || rel == "." || rel == ".." ||
		strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("refusing to clean %s: not a subdirectory of %s", absDir, absRoot)
	}
	return absDir, nil
}

func filterLocalTracks(tracks *[]*models.Track, preDownload bool) { // filter local tracks
	filteredTracks := (*tracks)[:0]

	for _, t := range *tracks {
		switch {
		case preDownload && !t.Present:
			// keep only unavailable tracks if c.FilterLocal is true
			filteredTracks = append(filteredTracks, t)

		case !preDownload && t.Present:
			// keep only tracks already present locally
			t.Present = false // reset so music system can reuse the field
			filteredTracks = append(filteredTracks, t)
		}
	}

	*tracks = filteredTracks
}

func getFilename(title, artist string) string {
	const maxBytes = 240

	// Remove illegal characters for file naming
	t := util.FilenameSafe(title)
	a := util.FilenameSafe(artist)

	// truncate long filename
	runes := []rune(fmt.Sprintf("%s-%s", t, a))
	for len(runes) > 0 && len(string(runes)) > maxBytes {
		runes = runes[:len(runes)-1]
	}

	return string(runes)
}

// ignore titles that have a specific keyword (defined in .env)
func ContainsKeyword(track models.Track, contentTitle string, filterList []string) bool {
	title := strings.ToLower(track.Title)
	artist := strings.ToLower(track.Artist)
	content := strings.ToLower(contentTitle)

	for _, keyword := range filterList {
		keyword = strings.ToLower(keyword)
		if strings.Contains(title, keyword) || strings.Contains(artist, keyword) {
			continue
		}
		if strings.Contains(content, keyword) {
			return true
		}
	}
	return false
}

func containsLower(str string, substr string) bool {

	return strings.Contains(
		strings.ToLower(str),
		strings.ToLower(substr),
	)
}

func sanitize(s string) string {
	s = strings.TrimSpace(s)

    replacer := strings.NewReplacer(
        "/", "-",
        "\\", "-",
        ":", "-",
        "*", "",
        "?", "",
        "\"", "",
        "<", "",
        ">", "",
        "|", "",
    )

    s = replacer.Replace(s)

    switch s {
    case ".", "..", ". .", "":
        return "_"
    }

    return s
}

func buildTrackPath(template string, track *models.Track) (string, error) {
	replacements := map[string]string{
		"Artist":		sanitize(track.MainArtist),
		"Album":		sanitize(track.Album),
		"AlbumName":	sanitize(track.Album),
		"TrackName":	sanitize(track.CleanTitle),
		"TrackNumber":	fmt.Sprintf("%02d", track.TrackNumber),
		"DiscNumber":	fmt.Sprintf("%02d", track.DiscNumber),
		"Year":			strconv.Itoa(track.OriginalYear),
		"File":			sanitize(track.File),
		"ext":			strings.TrimPrefix(filepath.Ext(track.File), "."),
	}

	result := template

	for key, value := range replacements {
		result = strings.ReplaceAll(
			result,
			"{{"+key+"}}",
			value,
		)
	}
	cleanPath := filepath.Clean(result)

	if !filepath.IsLocal(cleanPath) {
        return "", fmt.Errorf("path template resolves to a non-local path: %s", cleanPath)
    }

	file := filepath.Base(cleanPath)
	if ext := filepath.Ext(file); ext == "" {
		slog.Warn("path template does not have file extension ( {{ext}} ) appended, adding it automatically")
		file += filepath.Ext(track.File)
		cleanPath = filepath.Join(filepath.Dir(cleanPath), file)
	}

	track.File = file
	return cleanPath, nil
}

// overwriteMetadata rewrites srcFile's tags with metadata (if any) and, if coverPath is set,
// replaces its embedded cover with that image
func overwriteMetadata(metadata []string, coverPath, srcFile string) error {
	opts := ffmpeg.KwArgs{
			"c": "copy",
			"loglevel": "error",
		}
		if len(metadata) > 0 {
			opts["metadata"] = metadata
		}
		streams := []*ffmpeg.Stream{
    		ffmpeg.Input(srcFile),
		}
		if coverPath != "" { // map only the audio, dropping any old cover, plus the new picture
			streams = []*ffmpeg.Stream{ffmpeg.Input(srcFile).Audio(), ffmpeg.Input(coverPath).Video()}
			opts["disposition:v:0"] = "attached_pic"
			opts["metadata:s:v"] = []string{"title=Album cover", "comment=Cover (front)"}
		}

		tmpFile := tempAudioFile(srcFile)

		if err := util.WriteMetadata(streams, "", tmpFile, opts); err != nil {
			return fmt.Errorf("failed to overwrite metadata: %w", err)
		} else {
			if err := os.Rename(tmpFile, srcFile); err != nil {
				return fmt.Errorf("failed to rename tmp file: %w", err)
			}
		}
		return nil
}

func tempAudioFile(path string) string {
    ext := filepath.Ext(path)
    return strings.TrimSuffix(path, ext) + ".tmp" + ext
}


func moveTrack(srcFile, destDir string, track *models.Track, pathTemplate string, keepPerms bool) error {
	var dstFile string
    if pathTemplate != "" {
        relativePath, err := buildTrackPath(pathTemplate, track)
		if err != nil {
			return err
		}

        if track.File == "." || track.File == string(filepath.Separator) {
            track.File = getFilename(track.CleanTitle, track.MainArtist) + filepath.Ext(srcFile)
            relativePath = filepath.Join(filepath.Dir(relativePath), track.File)
            slog.Warn("invalid path template result",
                "track", track.Title,
                "artist", track.Artist,
                "filename", track.File)
        }

        dstFile = filepath.Join(destDir, relativePath)
    } else {
        if err := os.MkdirAll(destDir, os.ModePerm); err != nil {
            return err
        }
        dstFile = filepath.Join(destDir, track.File)
    }

    if err := moveFile(srcFile, dstFile, keepPerms); err != nil {
        return err
    }

    srcDir := filepath.Dir(srcFile)
    isEmpty, err := isDirEmpty(srcDir)
	if err != nil {
		return fmt.Errorf("couldn't check if directory is empty: %s", err.Error())
	} else if isEmpty {
		if err = os.Remove(srcDir); err != nil {
			return fmt.Errorf("failed to remove empty directory: %s", err.Error())
		}
	}

    return nil
}

func moveFile(srcFile, dstFile string, keepPermissions bool) error {
    in, err := os.Open(srcFile)
    if err != nil {
        return fmt.Errorf("couldn't open source file: %w", err)
    }
    defer func() {
    	if err := in.Close(); err != nil {
        	slog.Warn("failed to close source file", "err", err)
    	}
	}()
	if err := os.MkdirAll(filepath.Dir(dstFile), 0755); err != nil {
		return fmt.Errorf("couldn't create directory for file: %w", err)
	}
    out, err := os.Create(dstFile)
    if err != nil {
        return fmt.Errorf("couldn't create destination file: %w", err)
    }

    if _, err := io.Copy(out, in); err != nil {
        if closeErr := out.Close(); closeErr != nil {
        	slog.Warn("failed to close destination file", "err", closeErr)
    	}
        return fmt.Errorf("copy failed: %w", err)
    }

    if err := out.Sync(); err != nil {
        if closeErr := out.Close(); closeErr != nil {
        	slog.Warn("failed to close destination file", "err", closeErr)
    	}
        return fmt.Errorf("sync failed: %w", err)
    }

    if err := out.Close(); err != nil {
        return fmt.Errorf("failed to close destination file: %w", err)
    }

    if keepPermissions {
        info, err := os.Stat(srcFile)
        if err != nil {
            return fmt.Errorf("stat error: %w", err)
        }

        if err := os.Chmod(dstFile, info.Mode()); err != nil {
            return fmt.Errorf("chmod failed: %w", err)
        }
    }

    return os.Remove(srcFile)
}

func isDirEmpty(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer func() {
		if err = f.Close(); err != nil {
			slog.Error(fmt.Sprintf("failed to close directory path: %s", err.Error()))
		}
	}()

	// If we get something other than an err, it's not empty
	_, err = f.Readdir(1)
	if err == io.EOF {
		return true, nil // no entries
	}
	return false, err
}