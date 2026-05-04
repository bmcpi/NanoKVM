package firmware

// read.go provides cache-free read access to the firmware image without
// disturbing the USB mass-storage gadget.
//
// Strategy: open c.imagePath read-only with go-diskfs and walk the FAT
// in userspace. No kernel mount, no unpresent/present cycle, no driver
// state to go stale. Page-cache coherency between the gadget's writes
// (via f_mass_storage's fd) and our reads (via diskfs's fd) is provided
// by the kernel — both go through vfs_iter_read/write on the same inode.
//
// Used for env/inventory/boot-target reads that previously triggered a
// mount cycle. Writes still use the mount-based path in mount.go because
// vfat write semantics (FAT chain updates, dirent allocation) require a
// real driver.
//
// Caveats:
//   • A read that races a multi-sector U-Boot saveenv write may observe
//     a torn FAT. In practice U-Boot writes envs only at boot, and those
//     boots are infrequent, so this is acceptable.
//   • If go-diskfs can't open the image (e.g. download in progress),
//     callers fall back to a mount-based read.

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
	"sync"

	diskfs "github.com/diskfs/go-diskfs"

	"github.com/tinkerbell-community/NanoKVM/server/service/ubootenv"
)

// readMu serialises only the userspace FAT readers among themselves.
// They do not contend with c.mu (held by writers) — concurrent fresh
// reads while a write window is open are safe because the page cache
// arbitrates. We use a separate mutex so a long write doesn't block
// dashboard polling unnecessarily.
//
// In practice go-diskfs holds non-trivial in-memory FAT state per Open,
// and we open/close per call, so this just protects against thrashing
// /dev/loopXp1 access from many goroutines simultaneously.
var readMu sync.Mutex

// fatRelPath converts a host-side path under c.mountPoint to its FAT
// root-relative form (e.g. "/mnt/firmware/machine.env" → "/machine.env").
func (c *Controller) fatRelPath(hostPath string) string {
	rel := strings.TrimPrefix(hostPath, c.mountPoint)
	if rel == "" || rel[0] != '/' {
		rel = "/" + rel
	}
	return path.Clean(rel)
}

// readFileFresh opens the firmware image read-only and returns the
// contents of the named file from FAT partition 1. The name should be
// the FAT root-relative path (use c.fatRelPath to convert from a host
// path). Returns os.ErrNotExist if the file is missing.
//
// Does not require c.mu. Safe to call concurrently with anything except
// a download (which replaces the underlying file).
func (c *Controller) readFileFresh(fatPath string) ([]byte, error) {
	if !c.imageExists() {
		return nil, fmt.Errorf("firmware image not found: %s", c.imagePath)
	}

	readMu.Lock()
	defer readMu.Unlock()

	disk, err := diskfs.Open(c.imagePath)
	if err != nil {
		return nil, fmt.Errorf("open image: %w", err)
	}
	// disk.Backend implements io.Closer in v1.9.1; ignore if not present.
	defer func() {
		if closer, ok := any(disk).(io.Closer); ok {
			_ = closer.Close()
		}
	}()

	fs, err := disk.GetFilesystem(1)
	if err != nil {
		return nil, fmt.Errorf("get filesystem: %w", err)
	}

	f, err := fs.OpenFile(fatPath, os.O_RDONLY)
	if err != nil {
		// go-diskfs returns wrapped errors for missing files; normalise.
		if isFatNotFound(err) {
			return nil, os.ErrNotExist
		}
		return nil, fmt.Errorf("open %s: %w", fatPath, err)
	}
	defer func() {
		if closer, ok := any(f).(io.Closer); ok {
			_ = closer.Close()
		}
	}()

	return io.ReadAll(f)
}

// isFatNotFound returns true for go-diskfs errors that mean "file not in FAT".
func isFatNotFound(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, os.ErrNotExist) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "does not exist") || strings.Contains(msg, "not found")
}

// loadEnvFresh reads and parses a U-Boot env file from the image without
// mounting. Returns an empty Env when the file is missing.
func (c *Controller) loadEnvFresh(hostPath string) (*ubootenv.Env, error) {
	data, err := c.readFileFresh(c.fatRelPath(hostPath))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ubootenv.New(), nil
		}
		return nil, err
	}
	return ubootenv.Parse(data)
}
