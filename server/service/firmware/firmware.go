package firmware

import (
	"fmt"
	"os"
	"sync"

	"github.com/diskfs/go-diskfs/filesystem"
	log "github.com/sirupsen/logrus"

	"github.com/tinkerbell-community/NanoKVM/server/config"
	"github.com/tinkerbell-community/NanoKVM/server/service/ubootenv"
)

// Status describes the current state of the firmware controller.
type Status struct {
	Downloaded bool   `json:"downloaded"`
	Presented  bool   `json:"presented"`
	ImagePath  string `json:"imagePath"`
}

// Controller manages the firmware image lifecycle.
//
// The image file is presented to the USB mass storage gadget so the host
// (e.g. U-Boot) can boot from it. All env read/write operations use
// go-diskfs for direct FAT I/O — no kernel loop device or mount/umount is
// needed. Writes are immediately visible to the gadget because both paths
// share the same OS page cache for the image file.
type Controller struct {
	mu sync.Mutex

	imageURL  string
	imagePath string

	// FAT-root–relative paths (e.g. "/machine.env") derived from config.
	machineEnvFAT    string
	persistentEnvFAT string
	onceEnvFAT       string

	presented bool
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
			imageURL:         cfg.Firmware.ImageURL,
			imagePath:        cfg.Firmware.ImagePath,
			machineEnvFAT:    fatName(cfg.Firmware.MachineEnv),
			persistentEnvFAT: fatName(cfg.Firmware.PersistentEnv),
			onceEnvFAT:       fatName(cfg.Firmware.OnceEnv),
		}
	})
	return instance
}

// Init checks whether the firmware image is already available and presents
// it via the USB gadget. Call once at server startup.
func (c *Controller) Init() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.imageExists() {
		log.Info("firmware: image not found at ", c.imagePath)
		return nil
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

	return Status{
		Downloaded: c.imageExists(),
		Presented:  c.presented,
		ImagePath:  c.imagePath,
	}
}

// LoadEnv reads and parses machine.env written by U-Boot on the last boot.
// This is the source of truth for currently-effective variables.
func (c *Controller) LoadEnv() (*ubootenv.Env, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	var env *ubootenv.Env
	err := c.withDisk(func(fatFS filesystem.FileSystem) error {
		var e error
		env, e = c.readEnvFromDisk(fatFS, c.machineEnvFAT)
		return e
	})
	return env, err
}

// GetBootTarget returns the boot_targets value from persistent.env only.
// Returns an empty string when no persistent override is set.
func (c *Controller) GetBootTarget() (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	var target string
	err := c.withDisk(func(fatFS filesystem.FileSystem) error {
		env, e := c.readEnvFromDisk(fatFS, c.persistentEnvFAT)
		if e != nil {
			return fmt.Errorf("load persistent env: %w", e)
		}
		target, _ = env.Get(ubootenv.VarBootTargets)
		return nil
	})
	return target, err
}

// GetOnceBootTarget returns the boot_targets value from once.env.
// Returns an empty string when no one-shot override is pending.
func (c *Controller) GetOnceBootTarget() (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	var target string
	err := c.withDisk(func(fatFS filesystem.FileSystem) error {
		env, e := c.readEnvFromDisk(fatFS, c.onceEnvFAT)
		if e != nil {
			return fmt.Errorf("load once env: %w", e)
		}
		target, _ = env.Get(ubootenv.VarBootTargets)
		return nil
	})
	return target, err
}

// GetEffectiveBootTarget returns the boot_targets value from machine.env —
// the value that was actually in effect for the most recent boot.
func (c *Controller) GetEffectiveBootTarget() (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	var target string
	err := c.withDisk(func(fatFS filesystem.FileSystem) error {
		env, e := c.readEnvFromDisk(fatFS, c.machineEnvFAT)
		if e != nil {
			return fmt.Errorf("load machine env: %w", e)
		}
		target, _ = env.Get(ubootenv.VarBootTargets)
		return nil
	})
	return target, err
}

// SetBootTarget writes a persistent boot target override to persistent.env.
// An empty targets string clears the override.
func (c *Controller) SetBootTarget(targets string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.withDisk(func(fatFS filesystem.FileSystem) error {
		env, err := c.readEnvFromDisk(fatFS, c.persistentEnvFAT)
		if err != nil {
			return fmt.Errorf("load persistent env: %w", err)
		}
		if targets == "" {
			env.Delete(ubootenv.VarBootTargets)
		} else {
			env.Set(ubootenv.VarBootTargets, targets)
		}
		return c.writeEnvOrRemoveFromDisk(fatFS, env, c.persistentEnvFAT)
	})
}

// SetBootTargetOnce writes a one-shot boot target override to once.env.
// U-Boot imports the file on the next boot then removes it.
// An empty targets string clears any pending one-shot override.
func (c *Controller) SetBootTargetOnce(targets string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.withDisk(func(fatFS filesystem.FileSystem) error {
		env, err := c.readEnvFromDisk(fatFS, c.onceEnvFAT)
		if err != nil {
			return fmt.Errorf("load once env: %w", err)
		}
		if targets == "" {
			env.Delete(ubootenv.VarBootTargets)
		} else {
			env.Set(ubootenv.VarBootTargets, targets)
		}
		return c.writeEnvOrRemoveFromDisk(fatFS, env, c.onceEnvFAT)
	})
}

// GetInventory returns board inventory data from machine.env.
func (c *Controller) GetInventory() (map[string]string, error) {
	env, err := c.LoadEnv()
	if err != nil {
		return nil, err
	}
	return env.GetInventory(), nil
}

// GetAllEnvVars returns all variables from machine.env.
func (c *Controller) GetAllEnvVars() (map[string]string, error) {
	env, err := c.LoadEnv()
	if err != nil {
		return nil, err
	}
	return env.Vars, nil
}

func (c *Controller) imageExists() bool {
	_, err := os.Stat(c.imagePath)
	return err == nil
}
