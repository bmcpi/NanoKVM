package firmware

// diskimage.go provides direct FAT-image I/O using go-diskfs.
//
// All read/write operations go through go-diskfs, which manipulates the raw
// image bytes directly — no kernel loop device, no mount/umount, no root
// privilege required for the file access itself. The USB mass-storage gadget
// continues to present the same image file to the host; writes are immediately
// visible because both the BMC server and the gadget share the same OS page
// cache for the image file.

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	diskfslib "github.com/diskfs/go-diskfs"
	"github.com/diskfs/go-diskfs/filesystem"
	log "github.com/sirupsen/logrus"

	"github.com/tinkerbell-community/NanoKVM/server/service/ubootenv"
)

// fatName returns the FAT-root–relative path for an absolute host path.
// e.g. "/mnt/firmware/machine.env" → "/machine.env"
func fatName(absPath string) string {
	base := filepath.Base(absPath)
	if !strings.HasPrefix(base, "/") {
		return "/" + base
	}
	return base
}

// withDisk opens the firmware image via go-diskfs (no kernel mount), calls fn
// with the FAT filesystem on partition 1, then closes the disk.
// Must be called with c.mu held.
func (c *Controller) withDisk(fn func(fs filesystem.FileSystem) error) error {
	if !c.imageExists() {
		return fmt.Errorf("firmware image not found: %s", c.imagePath)
	}

	d, err := diskfslib.Open(c.imagePath)
	if err != nil {
		return fmt.Errorf("open disk image: %w", err)
	}
	defer func() {
		if err := d.Close(); err != nil {
			log.Debugf("firmware: disk close: %v", err)
		}
	}()

	fatFS, err := d.GetFilesystem(1)
	if err != nil {
		return fmt.Errorf("get FAT filesystem (partition 1): %w", err)
	}

	return fn(fatFS)
}

// readFromFS reads a complete file from the FAT filesystem.
// Returns (nil, nil) if the file does not exist.
func readFromFS(fatFS filesystem.FileSystem, fatPath string) ([]byte, error) {
	f, err := fatFS.OpenFile(fatPath, os.O_RDONLY)
	if err != nil {
		if isNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("open %s: %w", fatPath, err)
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", fatPath, err)
	}
	return data, nil
}

// writeToFS writes data to a file on the FAT filesystem, creating or truncating.
func writeToFS(fatFS filesystem.FileSystem, fatPath string, data []byte) error {
	f, err := fatFS.OpenFile(fatPath, os.O_RDWR|os.O_CREATE|os.O_TRUNC)
	if err != nil {
		return fmt.Errorf("open %s for write: %w", fatPath, err)
	}
	if _, err := io.Copy(f, bytes.NewReader(data)); err != nil {
		return fmt.Errorf("write %s: %w", fatPath, err)
	}
	return nil
}

// isNotExist returns true for go-diskfs file-not-found errors.
// go-diskfs v1.9.1 returns plain fmt.Errorf strings rather than sentinel
// errors, so errors.Is alone is insufficient.
func isNotExist(err error) bool {
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, os.ErrNotExist) {
		return true
	}
	s := err.Error()
	return strings.Contains(s, "does not exist") || strings.Contains(s, "not found")
}

// ---- env helpers (used by firmware.go for boot target management) ----------

// readEnvFromDisk reads and parses a U-Boot text env file from the FAT image.
// Returns an empty Env if the file does not exist.
func (c *Controller) readEnvFromDisk(fatFS filesystem.FileSystem, fatPath string) (*ubootenv.Env, error) {
	data, err := readFromFS(fatFS, fatPath)
	if err != nil {
		return nil, fmt.Errorf("read env %s: %w", fatPath, err)
	}
	if len(data) == 0 {
		return ubootenv.New(), nil
	}
	return ubootenv.Parse(data)
}

// writeEnvOrRemoveFromDisk serialises env to fatPath, or removes the file if
// env has no variables (so U-Boot doesn't try to import an empty file).
func (c *Controller) writeEnvOrRemoveFromDisk(fatFS filesystem.FileSystem, env *ubootenv.Env, fatPath string) error {
	if len(env.Vars) == 0 {
		if err := fatFS.Remove(fatPath); err != nil && !isNotExist(err) {
			return fmt.Errorf("remove %s: %w", fatPath, err)
		}
		return nil
	}
	return writeToFS(fatFS, fatPath, env.Marshal())
}

// ---- public file-level API --------------------------------------------------
//
// File operations target the firmware-files directory (c.firmwareDir), the
// canonical source-of-truth for FAT root contents. Each write/delete triggers
// a full image rebuild so the gadget serves the updated bytes. Env files
// written via SetBootTarget bypass this and go straight into the image.

// ReadFileFromImage reads a named file from the firmware-files directory.
// name may be relative ("u-boot.bin") or absolute ("/u-boot.bin").
// Returns (nil, nil) if the file does not exist.
func (c *Controller) ReadFileFromImage(name string) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	hostPath, err := c.firmwareHostPath(name)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(hostPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return data, nil
}

// WriteFileToImage writes data to a named file in the firmware-files
// directory and rebuilds the boot image. After the rebuild, sync(1) flushes
// the new image to disk so the USB gadget serves fresh data.
func (c *Controller) WriteFileToImage(name string, data []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	hostPath, err := c.firmwareHostPath(name)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(hostPath), 0o755); err != nil {
		return fmt.Errorf("mkdir parent: %w", err)
	}
	if err := os.WriteFile(hostPath, data, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", hostPath, err)
	}

	if err := c.buildImageLocked(); err != nil {
		return fmt.Errorf("rebuild image: %w", err)
	}
	_ = exec.Command("sync").Run()

	log.Infof("firmware: wrote %d bytes → %s (image rebuilt)", len(data), hostPath)
	return nil
}

// RemoveFileFromImage deletes a file from the firmware-files directory and
// rebuilds the image.
func (c *Controller) RemoveFileFromImage(name string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	hostPath, err := c.firmwareHostPath(name)
	if err != nil {
		return err
	}
	if err := os.Remove(hostPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove %s: %w", hostPath, err)
	}

	if err := c.buildImageLocked(); err != nil {
		return fmt.Errorf("rebuild image: %w", err)
	}
	_ = exec.Command("sync").Run()

	log.Infof("firmware: removed %s (image rebuilt)", hostPath)
	return nil
}

// ListFilesInImage returns names of all entries in the firmware-files dir
// (root level only).
func (c *Controller) ListFilesInImage() ([]string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.firmwareDir == "" {
		return nil, fmt.Errorf("firmwareDir not configured")
	}
	entries, err := os.ReadDir(c.firmwareDir)
	if err != nil {
		if os.IsNotExist(err) {
			return []string{}, nil
		}
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names, nil
}

// firmwareHostPath maps a user-supplied file name to its absolute path inside
// c.firmwareDir, rejecting traversal attempts.
func (c *Controller) firmwareHostPath(name string) (string, error) {
	if c.firmwareDir == "" {
		return "", fmt.Errorf("firmwareDir not configured")
	}
	clean := filepath.Clean("/" + strings.TrimPrefix(name, "/"))
	if strings.Contains(clean, "..") {
		return "", fmt.Errorf("invalid name %q", name)
	}
	return filepath.Join(c.firmwareDir, clean), nil
}

func ensureSlash(p string) string {
	if !strings.HasPrefix(p, "/") {
		return "/" + p
	}
	return p
}
