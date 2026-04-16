package firmware

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	log "github.com/sirupsen/logrus"
)

// mountImage creates a loop device for the firmware image and mounts the
// FAT partition at the configured mount point. Must be called with c.mu held.
// This is designed for short-lived mount/unmount cycles — the image is NOT
// kept permanently mounted so it doesn't conflict with the USB gadget.
func (c *Controller) mountImage() error {
	if err := os.MkdirAll(c.mountPoint, 0o755); err != nil {
		return fmt.Errorf("create mount point: %w", err)
	}

	// If already mounted from a previous incomplete cycle, reuse it.
	if isMounted(c.mountPoint) {
		log.Debug("firmware: mount point already in use, reusing")
		return nil
	}

	loopDev, err := c.setupLoop()
	if err != nil {
		return err
	}

	// The first partition is exposed as <loop>p1.
	partDev := loopDev + "p1"
	if _, err := os.Stat(partDev); err != nil {
		log.Warnf("firmware: %s not found, trying raw loop mount", partDev)
		partDev = loopDev
	}

	cmd := exec.Command("mount", "-t", "vfat", "-o", "rw,sync", partDev, c.mountPoint)
	if output, err := cmd.CombinedOutput(); err != nil {
		_ = exec.Command("losetup", "-d", loopDev).Run()
		return fmt.Errorf("mount %s: %s: %w", partDev, strings.TrimSpace(string(output)), err)
	}

	log.Debugf("firmware: mounted %s at %s", partDev, c.mountPoint)
	return nil
}

// setupLoop attaches the image to a free loop device with partition scanning.
// Returns the loop device path (e.g. "/dev/loop0").
// Compatible with BusyBox losetup which lacks --show.
func (c *Controller) setupLoop() (string, error) {
	out, err := exec.Command("losetup", "-f").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("losetup -f: %s: %w", strings.TrimSpace(string(out)), err)
	}
	dev := strings.TrimSpace(string(out))
	if dev == "" {
		return "", fmt.Errorf("losetup -f returned empty device path")
	}

	out, err = exec.Command("losetup", "-P", dev, c.imagePath).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("losetup -P %s %s: %s: %w", dev, c.imagePath, strings.TrimSpace(string(out)), err)
	}

	log.Debugf("firmware: loop device %s for %s", dev, c.imagePath)
	return dev, nil
}

// unmountImage unmounts the firmware partition, detaches the loop device,
// and drops page caches so the USB gadget serves fresh data.
// Must be called with c.mu held.
func (c *Controller) unmountImage() error {
	if !isMounted(c.mountPoint) {
		return nil
	}

	// Sync before unmounting to ensure all writes are flushed.
	_ = exec.Command("sync").Run()

	cmd := exec.Command("umount", "-d", c.mountPoint)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("umount: %s: %w", strings.TrimSpace(string(output)), err)
	}

	// Also detach any remaining loop device for this image.
	if loopDev := c.findLoopDevForImage(); loopDev != "" {
		if err := exec.Command("losetup", "-d", loopDev).Run(); err != nil {
			log.Warnf("firmware: losetup -d %s: %v", loopDev, err)
		}
	}

	// Drop page caches so the gadget's f_mass_storage re-reads from disk,
	// picking up any changes we wrote.
	_ = os.WriteFile("/proc/sys/vm/drop_caches", []byte("3"), 0o644)

	log.Debug("firmware: unmounted and flushed caches")
	return nil
}

// Unmount unmounts the firmware image (public, acquires lock).
func (c *Controller) Unmount() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.unmountImage()
}

// Mount mounts the firmware image (public, acquires lock).
func (c *Controller) Mount() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.mountImage()
}

// isMounted checks if a path is a mount point by reading /proc/mounts.
func isMounted(path string) bool {
	data, err := os.ReadFile("/proc/mounts")
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[1] == path {
			return true
		}
	}
	return false
}

// findLoopDevForImage scans /sys/block to find an existing loop device
// backing c.imagePath. Returns "" if none found.
func (c *Controller) findLoopDevForImage() string {
	entries, err := os.ReadDir("/sys/block")
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), "loop") {
			continue
		}
		backingFile := filepath.Join("/sys/block", e.Name(), "loop", "backing_file")
		data, err := os.ReadFile(backingFile)
		if err != nil {
			continue
		}
		if strings.TrimSpace(string(data)) == c.imagePath {
			return "/dev/" + e.Name()
		}
	}
	return ""
}
