package firmware

// builder.go composes the FAT boot image dynamically from the canonical
// firmware-files directory using go-diskfs.
//
// Workflow:
//   1. The firmware-files directory (c.firmwareDir) holds individual files
//      that should appear in the FAT root: u-boot.bin, config.txt, *.elf,
//      *.dat, *.dtb, overlays/*.dtbo, etc.
//   2. BuildImage() creates a fresh MBR-partitioned FAT32 image at
//      c.imagePath, sized to fit all firmware files plus headroom for
//      runtime env writes (machine.env, persistent.env, once.env).
//   3. Bootstrap() downloads the upstream reference image and extracts its
//      FAT root into c.firmwareDir, populating it on first run.
//
// Each file is independently versionable on the BMC's local filesystem;
// rebuilding the image replays them all into a clean FAT.

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	diskfslib "github.com/diskfs/go-diskfs"
	diskpkg "github.com/diskfs/go-diskfs/disk"
	"github.com/diskfs/go-diskfs/filesystem"
	"github.com/diskfs/go-diskfs/partition/mbr"
	log "github.com/sirupsen/logrus"
)

const (
	// MBR layout
	imageBlkSize        int64 = 512
	imagePartStartSect  int64 = 2048               // 1 MiB offset
	imageEnvHeadroom    int64 = 16 * 1024 * 1024   // 16 MB for env writes + slack
	imageMinSize        int64 = 256 * 1024 * 1024  // 256 MB floor
	imageVolumeLabel          = "NANOKVMFW"
	imageReadCopyBufSiz       = 64 * 1024
)

// BuildImage constructs the FAT image at c.imagePath from the contents of
// c.firmwareDir. Existing image is overwritten.
func (c *Controller) BuildImage() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buildImageLocked()
}

// buildImageLocked must be called with c.mu held.
func (c *Controller) buildImageLocked() error {
	if c.firmwareDir == "" {
		return fmt.Errorf("firmwareDir not configured")
	}
	if _, err := os.Stat(c.firmwareDir); err != nil {
		return fmt.Errorf("firmware dir %s: %w", c.firmwareDir, err)
	}

	// Tally file sizes (recursive) to size the FAT partition.
	var contentBytes int64
	var fileCount int
	err := filepath.Walk(c.firmwareDir, func(_ string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !info.IsDir() {
			contentBytes += info.Size()
			fileCount++
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("walk firmware dir: %w", err)
	}
	if fileCount == 0 {
		return fmt.Errorf("no files in %s", c.firmwareDir)
	}

	diskSize := contentBytes + contentBytes/2 + imageEnvHeadroom
	if diskSize < imageMinSize {
		diskSize = imageMinSize
	}
	// Round up to MiB.
	mib := int64(1024 * 1024)
	diskSize = ((diskSize + mib - 1) / mib) * mib

	// Preserve env files across rebuilds: read from old image (if any)
	// before destroying it; write back into new image at the end.
	preservedEnv := c.snapshotEnvFiles()

	// Recreate image file fresh — diskfs.Create requires the file not exist.
	if err := os.MkdirAll(filepath.Dir(c.imagePath), 0o755); err != nil {
		return fmt.Errorf("create image dir: %w", err)
	}
	// Unpresent gadget while rewriting the backing file.
	wasPresented := c.presented
	if wasPresented {
		if err := c.unpresentImage(); err != nil {
			log.Warnf("firmware: pre-build unpresent failed: %v", err)
		}
	}
	if err := os.Remove(c.imagePath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove old image: %w", err)
	}

	d, err := diskfslib.Create(c.imagePath, diskSize, diskfslib.SectorSizeDefault)
	if err != nil {
		return fmt.Errorf("create disk: %w", err)
	}
	closed := false
	defer func() {
		if !closed {
			_ = d.Close()
		}
	}()

	totalSectors := diskSize / imageBlkSize
	partSectors := uint32(totalSectors - imagePartStartSect - 1)
	table := &mbr.Table{
		Partitions: []*mbr.Partition{
			{
				Bootable: true,
				Type:     mbr.Fat32LBA,
				Start:    uint32(imagePartStartSect),
				Size:     partSectors,
			},
		},
	}
	if err := d.Partition(table); err != nil {
		return fmt.Errorf("partition: %w", err)
	}

	spec := diskpkg.FilesystemSpec{
		Partition:   1,
		FSType:      filesystem.TypeFat32,
		VolumeLabel: imageVolumeLabel,
	}
	fatFS, err := d.CreateFilesystem(spec)
	if err != nil {
		return fmt.Errorf("create FAT32: %w", err)
	}

	// Copy all files (recursive) from firmwareDir into FAT root.
	copied := 0
	err = filepath.Walk(c.firmwareDir, func(srcPath string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(c.firmwareDir, srcPath)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		fatPath := "/" + filepath.ToSlash(rel)
		if info.IsDir() {
			if err := fatFS.Mkdir(fatPath); err != nil {
				return fmt.Errorf("mkdir %s: %w", fatPath, err)
			}
			return nil
		}
		if err := copyHostFileIntoFAT(fatFS, srcPath, fatPath); err != nil {
			return fmt.Errorf("copy %s: %w", rel, err)
		}
		copied++
		return nil
	})
	if err != nil {
		return err
	}

	// Restore preserved env files into new image.
	for fatPath, data := range preservedEnv {
		if len(data) == 0 {
			continue
		}
		if err := writeToFS(fatFS, fatPath, data); err != nil {
			log.Warnf("firmware: restore env %s: %v", fatPath, err)
		}
	}

	closed = true
	if err := d.Close(); err != nil {
		return fmt.Errorf("close disk: %w", err)
	}

	log.Infof("firmware: built %s (%d MiB) with %d files from %s (preserved %d env)",
		c.imagePath, diskSize/mib, copied, c.firmwareDir, len(preservedEnv))

	if wasPresented {
		if err := c.presentImage(); err != nil {
			log.Warnf("firmware: post-build present failed: %v", err)
		}
	}
	return nil
}

// snapshotEnvFiles reads persistent.env and once.env from the existing image
// (if any) so they can be restored after a rebuild. Returns an empty map
// if the image does not exist or cannot be read.
func (c *Controller) snapshotEnvFiles() map[string][]byte {
	out := map[string][]byte{}
	if !c.imageExists() {
		return out
	}
	d, err := diskfslib.Open(c.imagePath)
	if err != nil {
		log.Debugf("firmware: snapshotEnvFiles open: %v", err)
		return out
	}
	defer d.Close()
	fatFS, err := d.GetFilesystem(1)
	if err != nil {
		log.Debugf("firmware: snapshotEnvFiles fs: %v", err)
		return out
	}
	for _, fp := range []string{c.persistentEnvFAT, c.onceEnvFAT} {
		if fp == "" {
			continue
		}
		data, err := readFromFS(fatFS, fp)
		if err == nil && len(data) > 0 {
			out[fp] = data
		}
	}
	return out
}

// copyHostFileIntoFAT streams an OS file into a FAT path.
func copyHostFileIntoFAT(fatFS filesystem.FileSystem, srcPath, fatPath string) error {
	src, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer src.Close()

	dst, err := fatFS.OpenFile(fatPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC)
	if err != nil {
		return err
	}
	buf := make([]byte, imageReadCopyBufSiz)
	if _, err := io.CopyBuffer(dst, src, buf); err != nil {
		return err
	}
	return nil
}

// extractImageToFirmwareDir opens srcImg via diskfs and copies all files from
// FAT root (recursive) into destDir on the host filesystem. Used by Bootstrap
// to seed the firmware-files directory from a downloaded reference image.
func extractImageToFirmwareDir(srcImg, destDir string) error {
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return fmt.Errorf("create dest dir: %w", err)
	}

	d, err := diskfslib.Open(srcImg)
	if err != nil {
		return fmt.Errorf("open source image: %w", err)
	}
	defer d.Close()

	fatFS, err := d.GetFilesystem(1)
	if err != nil {
		return fmt.Errorf("get filesystem: %w", err)
	}

	count, err := extractDir(fatFS, "/", destDir)
	if err != nil {
		return err
	}
	log.Infof("firmware: extracted %d files from %s into %s", count, srcImg, destDir)
	return nil
}

// extractDir recursively copies a FAT directory into a host directory.
func extractDir(fatFS filesystem.FileSystem, fatDir, hostDir string) (int, error) {
	entries, err := fatFS.ReadDir(fatDir)
	if err != nil {
		return 0, fmt.Errorf("readdir %s: %w", fatDir, err)
	}
	var count int
	for _, e := range entries {
		name := e.Name()
		if name == "." || name == ".." {
			continue
		}
		fatChild := strings.TrimRight(fatDir, "/") + "/" + name
		hostChild := filepath.Join(hostDir, name)

		if e.IsDir() {
			if err := os.MkdirAll(hostChild, 0o755); err != nil {
				return count, err
			}
			n, err := extractDir(fatFS, fatChild, hostChild)
			count += n
			if err != nil {
				return count, err
			}
			continue
		}

		if err := copyFATFileToHost(fatFS, fatChild, hostChild); err != nil {
			return count, fmt.Errorf("copy %s: %w", fatChild, err)
		}
		count++
	}
	return count, nil
}

// copyFATFileToHost streams a FAT file into an OS file.
func copyFATFileToHost(fatFS filesystem.FileSystem, fatPath, hostPath string) error {
	src, err := fatFS.OpenFile(fatPath, os.O_RDONLY)
	if err != nil {
		return err
	}

	dst, err := os.Create(hostPath)
	if err != nil {
		return err
	}
	defer dst.Close()

	buf := make([]byte, imageReadCopyBufSiz)
	if _, err := io.CopyBuffer(dst, src, buf); err != nil {
		return err
	}
	return dst.Sync()
}
