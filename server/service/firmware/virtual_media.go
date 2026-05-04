package firmware

// virtual_media.go implements virtual media management.
//
// When media is inserted the ISO file is copied into the firmware FAT image
// as "vm.iso". U-Boot loads that file via the blkmap driver. boot_targets is
// set to BootTargetVirtMedia ("blkmap") in once.env so the next boot picks it
// up automatically.
//
// When media is ejected vm.iso is removed from the FAT image and the
// once.env boot override is cleared.

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	log "github.com/sirupsen/logrus"

	"github.com/tinkerbell-community/NanoKVM/server/service/ubootenv"
)

// VirtualMediaState describes the current ISO insertion state.
type VirtualMediaState struct {
	Inserted  bool   `json:"inserted"`
	ImageName string `json:"imageName,omitempty"` // filename of the inserted ISO
	ImageSize int64  `json:"imageSize,omitempty"` // bytes
}

// GetVirtualMediaState returns the current virtual media state.
func (c *Controller) GetVirtualMediaState() VirtualMediaState {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.vmState
}

// vmISOName is the filename used inside the firmware FAT image for virtual media.
const vmISOName = "vm.iso"

// InsertVirtualMedia copies the named ISO into the firmware FAT image as
// "vm.iso" and sets boot_targets=blkmap in once.env so U-Boot's blkmap
// driver will load and boot it on the next power cycle.
// name must be a filename (not a path) inside c.mediaDir.
func (c *Controller) InsertVirtualMedia(name string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	defer c.invalidateReaderCacheLocked()

	if c.vmState.Inserted {
		return fmt.Errorf("virtual media already inserted: %s", c.vmState.ImageName)
	}

	isoPath, err := c.mediaPathFor(name)
	if err != nil {
		return err
	}
	info, err := os.Stat(isoPath)
	if err != nil {
		return fmt.Errorf("ISO not found: %w", err)
	}

	// Copy the ISO into the firmware FAT image as vm.iso, then update once.env
	// — all within a single mount window so we only unmount/present once.
	if err := c.withMount(func() error {
		destPath := filepath.Join(c.mountPoint, vmISOName)

		src, err := os.Open(isoPath)
		if err != nil {
			return fmt.Errorf("open ISO: %w", err)
		}
		defer src.Close()

		dst, err := os.Create(destPath)
		if err != nil {
			return fmt.Errorf("create vm.iso in image: %w", err)
		}
		defer dst.Close()

		if _, err := io.Copy(dst, src); err != nil {
			_ = os.Remove(destPath) // clean up partial file
			return fmt.Errorf("copy ISO to image: %w", err)
		}

		// Update once.env: boot_targets = blkmap.
		env, err := loadEnvFile(c.onceEnv)
		if err != nil {
			return err
		}
		env.Set(ubootenv.VarBootTargets, BootTargetVirtMedia)
		return saveOrRemoveEnv(env, c.onceEnv)
	}); err != nil {
		return fmt.Errorf("insert virtual media: %w", err)
	}

	c.vmState = VirtualMediaState{
		Inserted:  true,
		ImageName: name,
		ImageSize: info.Size(),
	}

	log.Infof("firmware: inserted virtual media %s (%d bytes) as %s", name, info.Size(), vmISOName)
	return nil
}

// EjectVirtualMedia removes vm.iso from the firmware FAT image and clears
// the blkmap boot_targets override from once.env.
func (c *Controller) EjectVirtualMedia() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	defer c.invalidateReaderCacheLocked()

	if !c.vmState.Inserted {
		return nil // idempotent
	}

	prevName := c.vmState.ImageName

	// Remove vm.iso from the FAT image and clear the boot override — one
	// mount window so we only need to unpresent/present once.
	if err := c.withMount(func() error {
		destPath := filepath.Join(c.mountPoint, vmISOName)
		if err := os.Remove(destPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove vm.iso from image: %w", err)
		}
		env, err := loadEnvFile(c.onceEnv)
		if err != nil {
			return err
		}
		env.Delete(ubootenv.VarBootTargets)
		return saveOrRemoveEnv(env, c.onceEnv)
	}); err != nil {
		return fmt.Errorf("eject virtual media: %w", err)
	}

	c.vmState = VirtualMediaState{}

	log.Infof("firmware: ejected virtual media %s", prevName)
	return nil
}

// ListMediaFiles returns the ISO filenames available in c.mediaDir.
func (c *Controller) ListMediaFiles() ([]string, error) {
	c.mu.Lock()
	dir := c.mediaDir
	c.mu.Unlock()

	if dir == "" {
		return nil, fmt.Errorf("mediaDir not configured")
	}
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return []string{}, nil
	}
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	return names, nil
}

// SaveMediaFile writes data to a new ISO file in c.mediaDir.
func (c *Controller) SaveMediaFile(name string, data []byte) error {
	c.mu.Lock()
	path, err := c.mediaPathFor(name)
	c.mu.Unlock()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir mediaDir: %w", err)
	}
	return os.WriteFile(path, data, 0o644)
}

// DeleteMediaFile removes the named ISO from c.mediaDir.
// Returns an error if the file is currently inserted.
func (c *Controller) DeleteMediaFile(name string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.vmState.Inserted && c.vmState.ImageName == name {
		return fmt.Errorf("cannot delete currently inserted media %q; eject first", name)
	}
	path, err := c.mediaPathFor(name)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove %s: %w", name, err)
	}
	return nil
}

// GetMediaDir returns the directory where ISOs are stored.
func (c *Controller) GetMediaDir() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.mediaDir
}

// mediaPathFor resolves a bare filename to its absolute path in c.mediaDir,
// rejecting any path traversal.
func (c *Controller) mediaPathFor(name string) (string, error) {
	if c.mediaDir == "" {
		return "", fmt.Errorf("mediaDir not configured")
	}
	base := filepath.Base(name)
	if base == "" || base == "." || strings.ContainsAny(base, "/\\") {
		return "", fmt.Errorf("invalid media filename %q", name)
	}
	return filepath.Join(c.mediaDir, base), nil
}
