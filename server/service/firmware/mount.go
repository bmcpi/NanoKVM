package firmware

import (
	"fmt"
	"os"
	"os/exec"
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
		log.Info("firmware: already mounted at ", c.mountPoint)
		return nil
	}

	// Mount the image. The image contains a single FAT partition starting at an offset.
	// Use losetup to find the partition offset, or mount with -o loop and let the kernel handle it.
	// For a single-partition image, we can try direct loop mount first.
	cmd := exec.Command("mount", "-o", "loop,rw", c.imagePath, c.mountPoint)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("mount: %s: %w", strings.TrimSpace(string(output)), err)
	}

	c.mounted = true
	log.Infof("firmware: mounted %s at %s", c.imagePath, c.mountPoint)
	return nil
}

// unmountImage unmounts the firmware partition. Must be called with c.mu held.
func (c *Controller) unmountImage() error {
	if !c.mounted {
		return nil
	}

	cmd := exec.Command("umount", c.mountPoint)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("umount: %s: %w", strings.TrimSpace(string(output)), err)
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
