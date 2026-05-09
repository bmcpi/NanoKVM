package firmware

// firmware.go contains the lifecycle Controller for the firmware boot image.
//
// Architecture:
//   - The image at c.imagePath is the canonical, bootable artefact. It is
//     downloaded as-is from c.imageURL (xz-compressed) on first run.
//   - The image is presented unchanged to the USB mass-storage gadget via
//     /sys/kernel/config/usb_gadget/g0/.../lun.0/file.
//   - All read/write access to the image's filesystem goes through a
//     mount cycle inside withMount(): unpresent → mount (offset-based loop) →
//     fn → sync → umount → drop_caches → present. No persistent loop device
//     is maintained; the kernel handles loop attachment internally as part of
//     `mount -o loop,offset=...`.
//   - Env reads are served from a small in-memory snapshot cache with a
//     short TTL so dashboard polling does not trigger a mount per request.
//     The cache is invalidated explicitly by every write method.
//   - c.firmwareDir is a host-side staging area mirroring files we want
//     to push into the image. SyncFirmwareDirToImage copies its contents
//     over the mounted image.

import (
	"errors"
	"fmt"
	"os"
	"sync"

	log "github.com/sirupsen/logrus"

	"github.com/BMCPi/NanoKVM/server/config"
	"github.com/BMCPi/NanoKVM/server/service/ubootenv"
)

// Status describes the current state of the firmware controller.
type Status struct {
	Downloaded    bool   `json:"downloaded"`
	Downloading   bool   `json:"downloading"`
	Presented     bool   `json:"presented"`
	ImagePath     string `json:"imagePath"`
	MountPoint    string `json:"mountPoint"`
	FirmwareDir   string `json:"firmwareDir"`
	FirmwareCount int    `json:"firmwareCount"`
}

// envSnapshot is a parsed view of all env files at one point in time.
type envSnapshot struct {
	uboot     *ubootenv.Env
	importEnv *ubootenv.Env
}

// Controller manages the firmware image lifecycle.
type Controller struct {
	mu sync.Mutex

	imageURL    string
	imagePath   string
	mountPoint  string
	firmwareDir string
	mediaDir    string // staging area for ISO files the user has uploaded

	// Host-OS paths for the U-Boot env files inside the FAT image.
	// ubootEnv  — binary env partition (CRC32 header + NUL-terminated pairs);
	//             U-Boot reads/writes this; BMC reads effective state from
	//             it and writes persistent changes back into it.
	// importEnv — plain-text one-shot override (formerly once.env); U-Boot
	//             applies it on the next boot and then deletes it.
	ubootEnv  string
	importEnv string

	presented bool

	reader  *readerCache      // cached read-only diskfs handle; nil = not open
	vmState VirtualMediaState // current virtual media insertion state
}

var (
	instance *Controller
	once     sync.Once
)

// GetController returns the singleton Controller, initializing it on first call.
func GetController() *Controller {
	once.Do(func() {
		cfg := config.GetInstance()
		instance = &Controller{
			imageURL:    cfg.Firmware.ImageURL,
			imagePath:   cfg.Firmware.ImagePath,
			mountPoint:  cfg.Firmware.MountPoint,
			firmwareDir: cfg.Firmware.FirmwareDir,
			mediaDir:    cfg.Firmware.MediaDir,
			ubootEnv:    cfg.Firmware.UbootEnv,
			importEnv:   cfg.Firmware.ImportEnv,
		}
	})
	return instance
}

// Init ensures an image exists (downloading if missing), attaches the
// persistent loop device, and presents the image via the USB gadget.
// Call once at server startup.
func (c *Controller) Init() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.imageExists() {
		log.Infof("firmware: image not found at %s, downloading", c.imagePath)
		if err := c.downloadImageLocked(); err != nil {
			return fmt.Errorf("download image: %w", err)
		}
	}

	// Create lun.1 (virtual CD-ROM) now, before the UDC is bound, so the
	// kernel accepts the topology change without needing an unbind/rebind.
	if err := c.ensureLUN1(); err != nil {
		log.Warnf("firmware: lun.1 setup failed (virtual media unavailable): %v", err)
	}

	log.Info("firmware: image found, presenting via USB gadget")
	if err := c.presentImage(); err != nil {
		log.Warnf("firmware: USB gadget present failed (may not be available in this environment): %v", err)
	}

	// Ensure a default empty machine.env exists in the image so U-Boot has
	// somewhere to persist saveenv writes. Best-effort.
	if err := c.ensureUbootEnvLocked(); err != nil {
		log.Warnf("firmware: ensure default machine.env: %v", err)
	}
	return nil
}

// GetStatus returns the current lifecycle state.
func (c *Controller) GetStatus() Status {
	c.mu.Lock()
	defer c.mu.Unlock()

	count := 0
	if entries, err := os.ReadDir(c.firmwareDir); err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				count++
			}
		}
	}

	return Status{
		Downloaded:    c.imageExists(),
		Downloading:   c.IsDownloading(),
		Presented:     c.presented,
		ImagePath:     c.imagePath,
		MountPoint:    c.mountPoint,
		FirmwareDir:   c.firmwareDir,
		FirmwareCount: count,
	}
}

func (c *Controller) imageExists() bool {
	info, err := os.Stat(c.imagePath)
	return err == nil && info.Size() > 0
}

// ---- env file helpers (host-FS paths under c.mountPoint) -------------------

// loadEnvFile reads and parses a U-Boot env file from the (mounted) image.
// Returns an empty Env when the file does not exist.
func loadEnvFile(path string) (*ubootenv.Env, error) {
	env, err := ubootenv.LoadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ubootenv.New(), nil
		}
		return nil, err
	}
	return env, nil
}

// saveOrRemoveEnv writes env to path, or deletes the file if env has no
// variables (so U-Boot doesn't try to import an empty file).
func saveOrRemoveEnv(env *ubootenv.Env, path string) error {
	if len(env.Vars) == 0 {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove %s: %w", path, err)
		}
		return nil
	}
	return env.SaveFile(path)
}

// ---- env snapshot (cache-free, page-cache-coherent reads) ----------------

// envSnapshotLocked reads both env files from the image without mounting.
// Each call is a fresh read via the userspace FAT parser. Must hold c.mu.
func (c *Controller) envSnapshotLocked() (*envSnapshot, error) {
	snap := &envSnapshot{}
	var err error
	if snap.uboot, err = c.loadUbootEnvFresh(); err != nil {
		return nil, fmt.Errorf("load uboot env: %w", err)
	}
	if snap.importEnv, err = c.loadEnvFresh(c.importEnv); err != nil {
		return nil, fmt.Errorf("load import env: %w", err)
	}
	return snap, nil
}

// ---- env API ---------------------------------------------------------------

// LoadEnv returns the parsed machine.env (the effective U-Boot environment).
// Fresh read.
func (c *Controller) LoadEnv() (*ubootenv.Env, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.loadUbootEnvFresh()
}

// BootTargets bundles the boot-target views read from the image.
type BootTargets struct {
	Effective string `json:"effective"` // from machine.env
	Import    string `json:"import"`    // from import.env (one-shot)
}

// GetBootTargets returns the effective and import boot targets in a
// single fresh read.
func (c *Controller) GetBootTargets() (BootTargets, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	snap, err := c.envSnapshotLocked()
	if err != nil {
		return BootTargets{}, err
	}
	bt := BootTargets{}
	bt.Effective, _ = snap.uboot.Get(ubootenv.VarBootTargets)
	bt.Import, _ = snap.importEnv.Get(ubootenv.VarBootTargets)
	return bt, nil
}

// GetBootTarget returns boot_targets from machine.env. Fresh read.
func (c *Controller) GetBootTarget() (string, error) {
	bt, err := c.GetBootTargets()
	return bt.Effective, err
}

// GetImportBootTarget returns boot_targets from import.env. Fresh read.
func (c *Controller) GetImportBootTarget() (string, error) {
	bt, err := c.GetBootTargets()
	return bt.Import, err
}

// GetEffectiveBootTarget returns boot_targets from machine.env. Fresh read.
func (c *Controller) GetEffectiveBootTarget() (string, error) {
	bt, err := c.GetBootTargets()
	return bt.Effective, err
}

// SetBootTarget writes a persistent boot target override to machine.env.
func (c *Controller) SetBootTarget(targets string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	defer c.invalidateReaderCacheLocked()

	return c.withMount(func() error {
		env, err := loadUbootEnvForWrite(c.ubootEnv)
		if err != nil {
			return fmt.Errorf("load uboot env: %w", err)
		}
		if targets == "" {
			env.Delete(ubootenv.VarBootTargets)
		} else {
			env.Set(ubootenv.VarBootTargets, targets)
		}
		return env.SaveFile(c.ubootEnv)
	})
}

// SetBootTargetOnce writes a one-shot boot target override to import.env.
func (c *Controller) SetBootTargetOnce(targets string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	defer c.invalidateReaderCacheLocked()

	return c.withMount(func() error {
		env, err := loadEnvFile(c.importEnv)
		if err != nil {
			return fmt.Errorf("load import env: %w", err)
		}
		if targets == "" {
			env.Delete(ubootenv.VarBootTargets)
		} else {
			env.Set(ubootenv.VarBootTargets, targets)
		}
		return saveOrRemoveEnv(env, c.importEnv)
	})
}

// GetInventory returns board inventory data from machine.env. Fresh read.
func (c *Controller) GetInventory() (map[string]string, error) {
	env, err := c.LoadEnv()
	if err != nil {
		return nil, err
	}
	return env.GetInventory(), nil
}

// GetAllEnvVars returns all variables from machine.env. Fresh read.
func (c *Controller) GetAllEnvVars() (map[string]string, error) {
	env, err := c.LoadEnv()
	if err != nil {
		return nil, err
	}
	return env.Vars, nil
}
