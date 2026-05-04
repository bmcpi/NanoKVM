package firmware

// firmware.go contains the lifecycle Controller for the firmware boot image.
//
// Architecture:
//   - The image at c.imagePath is the canonical, bootable artefact. It is
//     downloaded as-is from c.imageURL (xz-compressed) on first run.
//   - The image is presented unchanged to the USB mass-storage gadget via
//     /sys/kernel/config/usb_gadget/g0/.../lun.0/file.
//   - A persistent loop device (c.loopDev) is attached to the image at
//     Init and stays attached for the controller's lifetime. Loop attach
//     is just an fd open — it does not block the gadget from also serving
//     the same inode. See mount.go for the rationale.
//   - All read/write access to the image's filesystem goes through a
//     mount cycle inside withMount(): unpresent → mount loop partition →
//     fn → sync → umount → drop_caches → present. Loop stays attached.
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

	"github.com/tinkerbell-community/NanoKVM/server/config"
	"github.com/tinkerbell-community/NanoKVM/server/service/ubootenv"
)

// Status describes the current state of the firmware controller.
type Status struct {
	Downloaded    bool   `json:"downloaded"`
	Presented     bool   `json:"presented"`
	ImagePath     string `json:"imagePath"`
	MountPoint    string `json:"mountPoint"`
	FirmwareDir   string `json:"firmwareDir"`
	FirmwareCount int    `json:"firmwareCount"`
	LoopDevice    string `json:"loopDevice"`
}

// envSnapshot is a parsed view of all three env files at one point in time.
type envSnapshot struct {
	machine    *ubootenv.Env
	persistent *ubootenv.Env
	once       *ubootenv.Env
}

// Controller manages the firmware image lifecycle.
type Controller struct {
	mu sync.Mutex

	imageURL    string
	imagePath   string
	mountPoint  string
	firmwareDir string
	mediaDir    string

	// Full host-OS paths under c.mountPoint for the U-Boot env files.
	machineEnv    string
	persistentEnv string
	onceEnv       string

	loopDev   string // persistent loop device, attached at Init
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
			imageURL:      cfg.Firmware.ImageURL,
			imagePath:     cfg.Firmware.ImagePath,
			mountPoint:    cfg.Firmware.MountPoint,
			firmwareDir:   cfg.Firmware.FirmwareDir,
			mediaDir:      cfg.Firmware.MediaDir,
			machineEnv:    cfg.Firmware.MachineEnv,
			persistentEnv: cfg.Firmware.PersistentEnv,
			onceEnv:       cfg.Firmware.OnceEnv,
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

	// Persistent loop attach — saves ~250ms per subsequent mount cycle.
	if err := c.attachLoopLocked(); err != nil {
		log.Warnf("firmware: loop attach failed (will retry on first mount): %v", err)
	}

	log.Info("firmware: image found, presenting via USB gadget")
	if err := c.presentImage(); err != nil {
		log.Warnf("firmware: USB gadget present failed (may not be available in this environment): %v", err)
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
		Presented:     c.presented,
		ImagePath:     c.imagePath,
		MountPoint:    c.mountPoint,
		FirmwareDir:   c.firmwareDir,
		FirmwareCount: count,
		LoopDevice:    c.loopDev,
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

// envSnapshotLocked reads all three env files from the image without
// mounting. Each call is a fresh read via the userspace FAT parser,
// so it always reflects the current on-disk state (within page-cache
// coherency with any in-flight gadget writes). Must hold c.mu.
func (c *Controller) envSnapshotLocked() (*envSnapshot, error) {
	snap := &envSnapshot{}
	var err error
	if snap.machine, err = c.loadEnvFresh(c.machineEnv); err != nil {
		return nil, fmt.Errorf("load machine env: %w", err)
	}
	if snap.persistent, err = c.loadEnvFresh(c.persistentEnv); err != nil {
		return nil, fmt.Errorf("load persistent env: %w", err)
	}
	if snap.once, err = c.loadEnvFresh(c.onceEnv); err != nil {
		return nil, fmt.Errorf("load once env: %w", err)
	}
	return snap, nil
}

// ---- env API ---------------------------------------------------------------

// LoadEnv returns machine.env (written by U-Boot at last boot). Fresh read.
func (c *Controller) LoadEnv() (*ubootenv.Env, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.loadEnvFresh(c.machineEnv)
}

// BootTargets bundles the three boot-target views read from the image.
type BootTargets struct {
	Persistent string `json:"persistent"`
	Once       string `json:"once"`
	Effective  string `json:"effective"`
}

// GetBootTargets returns persistent, once, and effective boot targets in
// a single fresh read.
func (c *Controller) GetBootTargets() (BootTargets, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	snap, err := c.envSnapshotLocked()
	if err != nil {
		return BootTargets{}, err
	}
	bt := BootTargets{}
	bt.Persistent, _ = snap.persistent.Get(ubootenv.VarBootTargets)
	bt.Once, _ = snap.once.Get(ubootenv.VarBootTargets)
	bt.Effective, _ = snap.machine.Get(ubootenv.VarBootTargets)
	return bt, nil
}

// GetBootTarget returns boot_targets from persistent.env. Fresh read.
func (c *Controller) GetBootTarget() (string, error) {
	bt, err := c.GetBootTargets()
	return bt.Persistent, err
}

// GetOnceBootTarget returns boot_targets from once.env. Fresh read.
func (c *Controller) GetOnceBootTarget() (string, error) {
	bt, err := c.GetBootTargets()
	return bt.Once, err
}

// GetEffectiveBootTarget returns boot_targets from machine.env. Fresh read.
func (c *Controller) GetEffectiveBootTarget() (string, error) {
	bt, err := c.GetBootTargets()
	return bt.Effective, err
}

// SetBootTarget writes a continuous boot target override to persistent.env.
func (c *Controller) SetBootTarget(targets string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	defer c.invalidateReaderCacheLocked()

	return c.withMount(func() error {
		env, err := loadEnvFile(c.persistentEnv)
		if err != nil {
			return fmt.Errorf("load persistent env: %w", err)
		}
		if targets == "" {
			env.Delete(ubootenv.VarBootTargets)
		} else {
			env.Set(ubootenv.VarBootTargets, targets)
		}
		return saveOrRemoveEnv(env, c.persistentEnv)
	})
}

// SetBootTargetOnce writes a one-shot boot target override to once.env.
func (c *Controller) SetBootTargetOnce(targets string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	defer c.invalidateReaderCacheLocked()

	return c.withMount(func() error {
		env, err := loadEnvFile(c.onceEnv)
		if err != nil {
			return fmt.Errorf("load once env: %w", err)
		}
		if targets == "" {
			env.Delete(ubootenv.VarBootTargets)
		} else {
			env.Set(ubootenv.VarBootTargets, targets)
		}
		return saveOrRemoveEnv(env, c.onceEnv)
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
