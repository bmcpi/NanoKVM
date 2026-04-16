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
func (c *Controller) mountImage() error {
	if c.mounted {
		return nil
	}

	if err := os.MkdirAll(c.mountPoint, 0o755); err != nil {
		return fmt.Errorf("create mount point: %w", err)
	}

	// Check if already mounted (e.g. from a previous run).
	if isMounted(c.mountPoint) {
		c.mounted = true
		c.loopDev = c.findLoopDevForImage()
		log.Info("firmware: already mounted at ", c.mountPoint)
		return nil
	}

	// The image contains a partition table with a FAT partition.
	// Use losetup with --partscan to expose partition devices, then mount
	// the first partition.
	loopDev, err := c.setupLoop()
	if err != nil {
		return err
	}

	// The first partition is exposed as <loop>p1.
	partDev := loopDev + "p1"
	if _, err := os.Stat(partDev); err != nil {
		// No partition device — image may be a raw filesystem. Try the
		// loop device itself as a fallback.
		log.Warnf("firmware: %s not found, trying raw loop mount", partDev)
		partDev = loopDev
	}

	cmd := exec.Command("mount", "-t", "vfat", "-o", "rw", partDev, c.mountPoint)
	if output, err := cmd.CombinedOutput(); err != nil {
		// Clean up the loop device on failure.
		_ = exec.Command("losetup", "-d", loopDev).Run()
		return fmt.Errorf("mount %s: %s: %w", partDev, strings.TrimSpace(string(output)), err)
	}

	c.loopDev = loopDev
	c.mounted = true
	log.Infof("firmware: mounted %s (%s) at %s", partDev, c.imagePath, c.mountPoint)
	return nil
}

// setupLoop attaches the image to a free loop device with partition scanning.
// Returns the loop device path (e.g. "/dev/loop0").
// Compatible with BusyBox losetup which lacks --show.
func (c *Controller) setupLoop() (string, error) {
	// Step 1: Find a free loop device.
	// BusyBox: losetup -f  (prints next free device to stdout)
	out, err := exec.Command("losetup", "-f").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("losetup -f: %s: %w", strings.TrimSpace(string(out)), err)
	}
	dev := strings.TrimSpace(string(out))
	if dev == "" {
		return "", fmt.Errorf("losetup -f returned empty device path")
	}

	// Step 2: Attach the image to that device with partition scanning.
	// BusyBox supports: losetup [-rP] [-o OFS] {-f|LOOPDEV} FILE
	out, err = exec.Command("losetup", "-P", dev, c.imagePath).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("losetup -P %s %s: %s: %w", dev, c.imagePath, strings.TrimSpace(string(out)), err)
	}

	log.Infof("firmware: loop device %s for %s", dev, c.imagePath)
	return dev, nil
}

// unmountImage unmounts the firmware partition and detaches the loop device.
// Must be called with c.mu held.
func (c *Controller) unmountImage() error {
	if !c.mounted {
		return nil
	}

	cmd := exec.Command("umount", c.mountPoint)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("umount: %s: %w", strings.TrimSpace(string(output)), err)
	}

	// Detach the loop device to free it.
	if c.loopDev != "" {
		if err := exec.Command("losetup", "-d", c.loopDev).Run(); err != nil {
			log.Warnf("firmware: losetup -d %s: %v", c.loopDev, err)
		}
		c.loopDev = ""
	}

	c.mounted = false
	log.Infof("firmware: unmounted %s", c.mountPoint)
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
