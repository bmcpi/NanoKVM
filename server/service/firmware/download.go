package firmware

// download.go fetches the upstream U-Boot image (xz-compressed) and writes
// it directly to c.imagePath. No on-the-fly image construction is performed:
// the downloaded image is the canonical bootable artefact, byte-for-byte.

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

// Bootstrap downloads the upstream image (and decompresses if .xz) to
// c.imagePath. Idempotent: overwrites any existing image.
func (c *Controller) Bootstrap() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.downloadImageLocked()
}

// DownloadAndInit bootstraps from the upstream image, then presents via gadget.
func (c *Controller) DownloadAndInit() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := c.downloadImageLocked(); err != nil {
		return err
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

// downloadImageLocked downloads c.imageURL to c.imagePath. Must hold c.mu.
// Unpresents the gadget for the duration so writes to c.imagePath don't
// race with the gadget's view of the file.
func (c *Controller) downloadImageLocked() error {
	if _, err := os.Stat(downloadSentinel); err == nil {
		return fmt.Errorf("download already in progress")
	}
	if err := os.WriteFile(downloadSentinel, []byte("downloading"), 0o644); err != nil {
		return fmt.Errorf("create sentinel: %w", err)
	}
	defer os.Remove(downloadSentinel)

	if c.imageURL == "" {
		return fmt.Errorf("imageURL not configured")
	}
	if err := os.MkdirAll(filepath.Dir(c.imagePath), 0o755); err != nil {
		return fmt.Errorf("create image dir: %w", err)
	}

	// Release any gadget hold on the existing image.
	wasPresented := c.presented
	if wasPresented {
		if err := c.unpresentImage(); err != nil {
			log.Warnf("firmware: pre-download unpresent failed: %v", err)
		}
	}

	// Detach the persistent loop device so the inode is replaced cleanly.
	hadLoop := c.loopDev != ""
	if hadLoop {
		if err := c.detachLoopLocked(); err != nil {
			log.Warnf("firmware: pre-download loop detach: %v", err)
		}
	}

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

	log.Infof("firmware: downloading %s", c.imageURL)
	if err := downloadFile(c.imageURL, xzPath); err != nil {
		return fmt.Errorf("download: %w", err)
	}

	log.Info("firmware: decompressing image")
	if err := decompressXZ(xzPath, imgPath); err != nil {
		return fmt.Errorf("decompress: %w", err)
	}

	// Atomically replace the destination image.
	if err := moveFile(imgPath, c.imagePath); err != nil {
		return fmt.Errorf("install image: %w", err)
	}
	_ = exec.Command("sync").Run()

	log.Infof("firmware: installed image at %s", c.imagePath)

	// Re-attach loop device to the new image inode.
	if hadLoop {
		if err := c.attachLoopLocked(); err != nil {
			log.Warnf("firmware: post-download loop reattach: %v", err)
		}
	}

	// Re-present the (new) image for the gadget.
	if wasPresented {
		if err := c.presentImage(); err != nil {
			log.Warnf("firmware: post-download present failed: %v", err)
		}
	}
	return nil
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

// moveFile renames src to dest, falling back to copy+remove for cross-FS moves.
func moveFile(src, dest string) error {
	if err := os.Rename(src, dest); err == nil {
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dest)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Remove(src)
}
