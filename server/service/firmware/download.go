package firmware

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"

	log "github.com/sirupsen/logrus"
)

const downloadSentinel = "/tmp/.firmware_download_in_progress"

// Download fetches the firmware image from the configured URL, decompresses it,
// and stores it at the configured image path.
func (c *Controller) Download() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Prevent concurrent downloads.
	if _, err := os.Stat(downloadSentinel); err == nil {
		return fmt.Errorf("download already in progress")
	}

	if err := os.WriteFile(downloadSentinel, []byte("downloading"), 0o644); err != nil {
		return fmt.Errorf("create sentinel: %w", err)
	}
	defer os.Remove(downloadSentinel)

	dir := filepath.Dir(c.imagePath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create firmware dir: %w", err)
	}

	xzPath := c.imagePath + ".xz"

	log.Infof("firmware: downloading %s", c.imageURL)
	if err := downloadFile(c.imageURL, xzPath); err != nil {
		os.Remove(xzPath)
		return fmt.Errorf("download: %w", err)
	}

	log.Info("firmware: decompressing image")
	if err := decompressXZ(xzPath, c.imagePath); err != nil {
		os.Remove(xzPath)
		os.Remove(c.imagePath)
		return fmt.Errorf("decompress: %w", err)
	}

	// Clean up compressed file.
	os.Remove(xzPath)

	log.Info("firmware: download complete")
	return nil
}

// DownloadAndInit downloads the firmware image, then mounts and presents it.
func (c *Controller) DownloadAndInit() error {
	if err := c.Download(); err != nil {
		return err
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if err := c.mountImage(); err != nil {
		return fmt.Errorf("mount after download: %w", err)
	}

	if err := c.presentImage(); err != nil {
		log.Warnf("firmware: USB gadget present failed: %v", err)
	}

	return nil
}

// IsDownloading returns true if a download is in progress.
func (c *Controller) IsDownloading() bool {
	_, err := os.Stat(downloadSentinel)
	return err == nil
}

func downloadFile(url, dest string) error {
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

func decompressXZ(src, dest string) error {
	// Use xz command to decompress: xz -d -k keeps source, writes to stdout.
	cmd := exec.Command("xz", "-d", "-c", src)
	outFile, err := os.Create(dest)
	if err != nil {
		return fmt.Errorf("create output: %w", err)
	}
	defer outFile.Close()

	cmd.Stdout = outFile
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("xz decompress: %w", err)
	}

	return outFile.Sync()
}
