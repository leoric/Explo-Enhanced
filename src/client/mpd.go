package client

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"log/slog"
	"strings"

	"explo/src/config"
	"explo/src/models"
)

type MPD struct {
	Cfg config.ClientConfig
}

func NewMPD(cfg config.ClientConfig) *MPD {
	return &MPD{Cfg: cfg}
}

// playlistPath returns the .m3u path, with path separators in the playlist name replaced
func (c *MPD) playlistPath() string {
	name := strings.NewReplacer("/", "_", "\\", "_").Replace(c.Cfg.PlaylistName)
	return c.Cfg.PlaylistDir + name + ".m3u"
}

func (c *MPD) GetLibrary() error {
	return nil
}

func (c *MPD) GetAuth() error {
	return nil
}

func (c *MPD) AddHeader() error {
	return nil
}

func (c *MPD) AddLibrary() error {
	return nil
}

func (c *MPD) SearchSongs(tracks []*models.Track) error {
	for i := range tracks {
		if tracks[i].File == "" {
			continue
		}
	
		if c.Cfg.DownloadDir != "" {
			fullName := tracks[i].File
			if fullPath, err := c.findTrack(fullName, c.Cfg.DownloadDir); err == nil {
				tracks[i].File = fullPath
				tracks[i].Present = true
				continue
			} else {
				fmt.Printf("Track not found in DownloadDir: %s\n", fullName)
			}
		}
	}
	return nil
}

func (c *MPD) RefreshLibrary() error {
	return nil
}

func (c *MPD) CheckRefreshState() bool {
	return true
}

func (c *MPD) CreatePlaylist(tracks []*models.Track) error {
	f, err := os.OpenFile(c.playlistPath(), os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0666)
	if err != nil {
		return err
	}

	for _, track := range tracks {
		if track.Present {
			_, err := f.Write([]byte(track.File+"\n"))
			if err != nil {
				slog.Warn(fmt.Sprintf("failed to write song to file: %s", err.Error()))
			}
		}
	}
	return nil
}

func (c *MPD) SearchPlaylist() error {
	if _, err := os.Stat(c.playlistPath()); errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("did not find playlist: %s", c.Cfg.PlaylistName)
	} else {
		c.Cfg.PlaylistID = c.playlistPath()
		return nil
	}
}

func (c *MPD) UpdatePlaylist() error {
	return nil
}

func (c *MPD) DeletePlaylist() error {
	if c.Cfg.PlaylistID != "" {
		if err := os.Remove(c.Cfg.PlaylistID); err != nil {
			return fmt.Errorf("failed to delete playlist: %s", err.Error())
		}
		return nil
	}
	return fmt.Errorf("playlist not found")
}

func (c MPD) findTrack(name, path string) (string, error) {
	var foundPath string
    errorFound := errors.New("file found")
    err := filepath.WalkDir(path, func(currentPath string, d os.DirEntry, err error) error {
    if err != nil {
        return err
    }
    if d.Name() == name {
		foundPath = currentPath
        return errorFound
    }
    return nil
   })
   if errors.Is(err, errorFound) {
		return foundPath, nil
   }

   return "", fmt.Errorf("no file found named %s in %s: %s", name, path, err)
}