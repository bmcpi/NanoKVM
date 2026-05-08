package firmware

// update.go orchestrates u-boot firmware updates: query latest release,
// determine if an update is needed, download the new image while
// preserving env files (machine.env, persistent.env, once.env) from the
// existing image.

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"

	log "github.com/sirupsen/logrus"
	"github.com/ulikunitz/xz"
)

// VersionInfo describes the current and latest u-boot versions.
type VersionInfo struct {
	Current         string `json:"current"`
	Latest          string `json:"latest"`
	UpdateAvailable bool   `json:"updateAvailable"`
	AssetURL        string `json:"assetUrl,omitempty"`
}

// envFileFATPaths are the FAT root-relative paths of env files we
// preserve across firmware updates.
var envFileFATPaths = []string{"/machine.env", "/persistent.env", "/once.env"}

// GetUBootVersionInfo returns the currently-running u-boot version (read
// from machine.env's `ver` variable) and the latest available release.
func (c *Controller) GetUBootVersionInfo() (VersionInfo, error) {
	c.mu.Lock()
	current := ""
	if env, err := c.loadEnvFresh(c.machineEnv); err == nil {
		if v, ok := env.Get("ver"); ok {
			current = parseUBootVer(v)
		}
	}
	c.mu.Unlock()

	info := VersionInfo{Current: current}
	rel, err := LatestUBootRelease()
	if err != nil {
		return info, err
	}
	info.Latest = rel.Version
	info.AssetURL = rel.AssetURL
	if current == "" {
		info.UpdateAvailable = true
	} else {
		info.UpdateAvailable = CompareUBootVersions(rel.Version, current) > 0
	}
	return info, nil
}

// parseUBootVer extracts a "vMAJOR.MINOR[-rcN]" token from U-Boot's
// `ver` env variable, which typically looks like:
//
//	"U-Boot 2026.07-rc1 (Aug 28 2025 - 12:34:56 +0000)"
//	"U-Boot v2026.07 (...)"
//
// Returns the version with a leading "v" so it compares cleanly against
// release tags.
func parseUBootVer(s string) string {
	for _, tok := range strings.Fields(s) {
		t := strings.TrimPrefix(tok, "v")
		if t == "" {
			continue
		}
		if !(t[0] >= '0' && t[0] <= '9') {
			continue
		}
		// Looks like a version token (starts with a digit, contains a dot).
		if strings.Contains(t, ".") {
			return "v" + t
		}
	}
	return ""
}

// UpdateUBoot downloads the latest u-boot image and installs it,
// preserving the three env files from the existing image. If url is
// empty, the latest release URL is resolved automatically.
func (c *Controller) UpdateUBoot() error {
	rel, err := LatestUBootRelease()
	if err != nil {
		return fmt.Errorf("resolve latest release: %w", err)
	}
	return c.UpdateUBootFromURL(rel.AssetURL)
}

// UpdateUBootFromURL replaces the current image with the .img.xz at the
// given URL, preserving env files.
func (c *Controller) UpdateUBootFromURL(url string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if url == "" {
		return fmt.Errorf("empty url")
	}

	// 1. Snapshot env files from the existing image (best-effort).
	preserved := make(map[string][]byte)
	if c.imageExists() {
		for _, p := range envFileFATPaths {
			data, err := c.readFileFresh(p)
			if err != nil {
				if !errors.Is(err, os.ErrNotExist) {
					log.Warnf("firmware: pre-update read %s failed: %v", p, err)
				}
				continue
			}
			preserved[p] = data
			log.Debugf("firmware: preserved %s (%d bytes)", p, len(data))
		}
	}

	// 2. Download & install the new image (replaces c.imagePath atomically).
	if err := c.downloadFromURLLocked(url); err != nil {
		return err
	}

	// 3. Restore preserved env files into the new image.
	if len(preserved) > 0 {
		if err := c.withMount(func() error {
			for fatPath, data := range preserved {
				dest := filepath.Join(c.mountPoint, filepath.FromSlash(strings.TrimPrefix(fatPath, "/")))
				if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
					return fmt.Errorf("mkdir %s: %w", filepath.Dir(dest), err)
				}
				if err := os.WriteFile(dest, data, 0o644); err != nil {
					return fmt.Errorf("restore %s: %w", dest, err)
				}
				log.Infof("firmware: restored %s (%d bytes)", path.Base(fatPath), len(data))
			}
			return nil
		}); err != nil {
			log.Warnf("firmware: env restore failed: %v", err)
			return fmt.Errorf("restore envs: %w", err)
		}
	}
	return nil
}

// downloadFromURLLocked is identical to downloadImageLocked but takes an
// explicit URL (used by the upgrade flow). Must hold c.mu.
func (c *Controller) downloadFromURLLocked(url string) error {
	if _, err := os.Stat(downloadSentinel); err == nil {
		return fmt.Errorf("download already in progress")
	}
	if err := os.WriteFile(downloadSentinel, []byte("downloading"), 0o644); err != nil {
		return fmt.Errorf("create sentinel: %w", err)
	}
	defer os.Remove(downloadSentinel)

	if err := os.MkdirAll(filepath.Dir(c.imagePath), 0o755); err != nil {
		return fmt.Errorf("create image dir: %w", err)
	}

	wasPresented := c.presented
	if wasPresented {
		if err := c.unpresentImage(); err != nil {
			log.Warnf("firmware: pre-download unpresent failed: %v", err)
		}
	}
	hadLoop := c.loopDev != ""
	if hadLoop {
		if err := c.detachLoopLocked(); err != nil {
			log.Warnf("firmware: pre-download loop detach: %v", err)
		}
	}
	c.invalidateReaderCacheLocked()

	stageDir := filepath.Join(filepath.Dir(c.imagePath), "stage")
	if err := os.MkdirAll(stageDir, 0o755); err != nil {
		return fmt.Errorf("create stage dir: %w", err)
	}
	xzPath := filepath.Join(stageDir, "upstream.img.xz")
	imgPath := filepath.Join(stageDir, "upstream.img")
	defer func() {
		_ = os.Remove(xzPath)
		_ = os.Remove(imgPath)
	}()

	log.Infof("firmware: downloading u-boot image from %s", url)
	if err := downloadFileTo(url, xzPath); err != nil {
		return fmt.Errorf("download: %w", err)
	}

	log.Info("firmware: decompressing image")
	if err := decompressXZTo(xzPath, imgPath); err != nil {
		return fmt.Errorf("decompress: %w", err)
	}
	if err := moveFile(imgPath, c.imagePath); err != nil {
		return fmt.Errorf("install image: %w", err)
	}
	_ = exec.Command("sync").Run()
	log.Infof("firmware: installed image at %s", c.imagePath)

	if hadLoop {
		if err := c.attachLoopLocked(); err != nil {
			log.Warnf("firmware: post-download loop reattach: %v", err)
		}
	}
	if wasPresented {
		if err := c.presentImage(); err != nil {
			log.Warnf("firmware: post-download present failed: %v", err)
		}
	}
	return nil
}

// downloadFileTo is exported-style helper used by downloadFromURLLocked.
// It mirrors downloadFile() in download.go but with a parameterised URL.
func downloadFileTo(url, dest string) error {
	resp, err := http.Get(url)
	if err != nil {
		return fmt.Errorf("HTTP GET: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, resp.Status)
	}
	f, err := os.Create(dest)
	if err != nil {
		return fmt.Errorf("create file: %w", err)
	}
	defer f.Close()
	written, err := io.Copy(f, resp.Body)
	if err != nil {
		return fmt.Errorf("write: %w", err)
	}
	log.Infof("firmware: downloaded %d bytes", written)
	return f.Sync()
}

func decompressXZTo(src, dest string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open xz: %w", err)
	}
	defer in.Close()
	r, err := xz.NewReader(in)
	if err != nil {
		return fmt.Errorf("xz reader: %w", err)
	}
	out, err := os.Create(dest)
	if err != nil {
		return fmt.Errorf("create output: %w", err)
	}
	defer out.Close()
	if _, err := io.Copy(out, r); err != nil {
		return fmt.Errorf("xz decompress: %w", err)
	}
	return out.Sync()
}
