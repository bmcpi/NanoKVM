package firmware

// read.go provides cache-free read access to the firmware image without
// disturbing the USB mass-storage gadget.
//
// Strategy: open c.imagePath read-only with go-diskfs and walk the FAT
// in userspace. No kernel mount, no unpresent/present cycle.
//
// Performance: parsing the FAT on every call is expensive, so the
// *disk.Disk and *filesystem.FileSystem are cached on the controller.
// The cache is invalidated on every write (so subsequent reads see our
// own changes) and on download (image inode replaced). Concurrent
// dashboard polls within one write/download window all reuse the same
// open handle and only pay for the cluster-walk + read of the target
// file's data.
//
// Page-cache coherency between gadget writes (via f_mass_storage's fd)
// and our reads (via diskfs's fd on the same inode) is provided by the
// kernel — both go through vfs_iter_read/write on the same struct file's
// underlying inode.
//
// Caveats:
//   • A read that races a multi-sector U-Boot saveenv write may observe
//     a torn FAT. In practice U-Boot writes envs only at boot.
//   • Host-side mutations between two BMC reads will NOT be reflected
//     until the cache is dropped (which currently happens only on our
//     own writes). For env files this is acceptable because the host
//     only writes them on boot transitions, and the read-cache horizon
//     is the dashboard refresh interval.

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"

	diskfs "github.com/diskfs/go-diskfs"
	"github.com/diskfs/go-diskfs/disk"
	"github.com/diskfs/go-diskfs/filesystem"

	"github.com/tinkerbell-community/NanoKVM/server/service/ubootenv"
)

// readerCache holds a parsed view of the firmware image. Lifetime is
// bounded by writes and downloads; see invalidateReaderCacheLocked.
type readerCache struct {
	disk *disk.Disk
	fs   filesystem.FileSystem
}

// readerLocked returns a cached reader, opening the image if needed.
// Must hold c.mu.
func (c *Controller) readerLocked() (*readerCache, error) {
	if c.reader != nil {
		return c.reader, nil
	}
	if !c.imageExists() {
		return nil, fmt.Errorf("firmware image not found: %s", c.imagePath)
	}

	d, err := diskfs.Open(c.imagePath, diskfs.WithOpenMode(diskfs.ReadOnly))
	if err != nil {
		return nil, fmt.Errorf("open image: %w", err)
	}
	fs, err := d.GetFilesystem(1)
	if err != nil {
		_ = d.Close()
		return nil, fmt.Errorf("get filesystem: %w", err)
	}

	c.reader = &readerCache{disk: d, fs: fs}
	return c.reader, nil
}

// invalidateReaderCacheLocked closes and clears the cached reader.
// Call after any write or download. Must hold c.mu.
func (c *Controller) invalidateReaderCacheLocked() {
	if c.reader != nil {
		_ = c.reader.disk.Close()
		c.reader = nil
	}
}

// fatRelPath converts a host-side path under c.mountPoint to its FAT
// root-relative form (e.g. "/mnt/firmware/machine.env" → "/machine.env").
func (c *Controller) fatRelPath(hostPath string) string {
	rel := strings.TrimPrefix(hostPath, c.mountPoint)
	if rel == "" || rel[0] != '/' {
		rel = "/" + rel
	}
	return path.Clean(rel)
}

// readFileFresh reads a file from the firmware image's FAT root via the
// cached userspace reader. Returns os.ErrNotExist if missing. Must hold c.mu.
func (c *Controller) readFileFresh(fatPath string) ([]byte, error) {
	r, err := c.readerLocked()
	if err != nil {
		return nil, err
	}

	f, err := r.fs.OpenFile(fatPath, os.O_RDONLY)
	if err != nil {
		if isFatNotFound(err) {
			return nil, os.ErrNotExist
		}
		// A stale cached fs may fail to open a file that exists. Drop the
		// cache and retry once.
		c.invalidateReaderCacheLocked()
		r, err2 := c.readerLocked()
		if err2 != nil {
			return nil, err
		}
		f, err = r.fs.OpenFile(fatPath, os.O_RDONLY)
		if err != nil {
			if isFatNotFound(err) {
				return nil, os.ErrNotExist
			}
			return nil, fmt.Errorf("open %s: %w", fatPath, err)
		}
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
// mounting. Returns an empty Env when the file is missing. Must hold c.mu.
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
