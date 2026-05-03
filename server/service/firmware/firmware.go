package firmware

import (
	"fmt"
	"os"
	"sync"

	log "github.com/sirupsen/logrus"

	"github.com/tinkerbell-community/NanoKVM/server/config"
	"github.com/tinkerbell-community/NanoKVM/server/service/ubootenv"
)

// Status describes the current state of the firmware controller.
type Status struct {
	Downloaded bool   `json:"downloaded"`
	Presented  bool   `json:"presented"`
	ImagePath  string `json:"imagePath"`
	MountPoint string `json:"mountPoint"`
}

// Controller manages the firmware image lifecycle.
//
// The image file is presented directly to the USB mass storage gadget so the
// host (e.g. U-Boot) can boot from it. The controller does NOT keep the image
// permanently mounted; instead it mounts on demand for env read/write
// operations and unmounts immediately afterwards. This avoids conflicts
// between the gadget's file-backed I/O and a local filesystem mount.
type Controller struct {
	mu sync.Mutex

	imageURL   string
	imagePath  string
	mountPoint string

	// machineEnv is the file U-Boot writes on every boot containing the full
	// effective environment. Read-only from our side; used for inventory and
	// for reporting the currently-applied boot target.
	machineEnv string
	// persistentEnv contains overrides U-Boot imports on every boot.
	persistentEnv string
	// onceEnv contains one-shot overrides; U-Boot imports it then deletes it.
	onceEnv string

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
			imageURL:      cfg.Firmware.ImageURL,
			imagePath:     cfg.Firmware.ImagePath,
			mountPoint:    cfg.Firmware.MountPoint,
			machineEnv:    cfg.Firmware.MachineEnv,
			persistentEnv: cfg.Firmware.PersistentEnv,
			onceEnv:       cfg.Firmware.OnceEnv,
		}
	})
	return instance
}

// Init checks whether the firmware image is already available and presents
// it via the USB gadget. The image is NOT mounted permanently — env
// operations mount on demand. Call once at server startup.
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
		MountPoint: c.mountPoint,
	}
}

// withMount temporarily mounts the firmware image, calls fn, then unmounts.
// This is the only way env operations access the filesystem to avoid
// conflicts with the USB gadget's file-backed I/O path. Must be called
// with c.mu held.
func (c *Controller) withMount(fn func() error) error {
	if err := c.mountImage(); err != nil {
		return fmt.Errorf("mount: %w", err)
	}
	defer func() {
		if err := c.unmountImage(); err != nil {
			log.Warnf("firmware: deferred unmount failed: %v", err)
		}
	}()
	return fn()
}

// LoadEnv reads and parses the machine.env file written by U-Boot on the last
// boot. This is the source of truth for currently-effective variables.
func (c *Controller) LoadEnv() (*ubootenv.Env, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	var env *ubootenv.Env
	err := c.withMount(func() error {
		var e error
		env, e = ubootenv.LoadFile(c.machineEnv)
		return e
	})
	return env, err
}

// loadOverrideLocked reads an override env file (persistent.env or once.env).
// Returns an empty Env when the file does not exist. Must hold c.mu.
func (c *Controller) loadOverrideLocked(path string) (*ubootenv.Env, error) {
	env, err := ubootenv.LoadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return ubootenv.New(), nil
		}
		return nil, err
	}
	return env, nil
}

// saveOrRemoveLocked writes env to path, or deletes the file if env has no
// variables (so U-Boot doesn't try to import an empty file). Must hold c.mu.
func (c *Controller) saveOrRemoveLocked(env *ubootenv.Env, path string) error {
	if len(env.Vars) == 0 {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove %s: %w", path, err)
		}
		return nil
	}
	return env.SaveFile(path)
}

// GetBootTarget returns the effective boot_targets value: an active
// persistent.env override takes precedence; otherwise the value from
// machine.env is returned. Returns an empty string when neither is set.
func (c *Controller) GetBootTarget() (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	var target string
	err := c.withMount(func() error {
		if pers, err := c.loadOverrideLocked(c.persistentEnv); err != nil {
			return fmt.Errorf("load persistent env: %w", err)
		} else if v, ok := pers.Get(ubootenv.VarBootTargets); ok {
			target = v
			return nil
		}

		machine, err := ubootenv.LoadFile(c.machineEnv)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return fmt.Errorf("load machine env: %w", err)
		}
		target, _ = machine.Get(ubootenv.VarBootTargets)
		return nil
	})
	return target, err
}

// SetBootTarget writes a continuous boot target override to persistent.env.
// An empty targets string clears the override.
func (c *Controller) SetBootTarget(targets string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.withMount(func() error {
		env, err := c.loadOverrideLocked(c.persistentEnv)
		if err != nil {
			return fmt.Errorf("load persistent env: %w", err)
		}

		if targets == "" {
			env.Delete(ubootenv.VarBootTargets)
		} else {
			env.Set(ubootenv.VarBootTargets, targets)
		}

		return c.saveOrRemoveLocked(env, c.persistentEnv)
	})
}

// SetBootTargetOnce writes a one-shot boot target override to once.env. U-Boot
// imports the file on the next boot then removes it. An empty targets string
// clears any pending one-shot override.
func (c *Controller) SetBootTargetOnce(targets string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.withMount(func() error {
		env, err := c.loadOverrideLocked(c.onceEnv)
		if err != nil {
			return fmt.Errorf("load once env: %w", err)
		}

		if targets == "" {
			env.Delete(ubootenv.VarBootTargets)
		} else {
			env.Set(ubootenv.VarBootTargets, targets)
		}

		return c.saveOrRemoveLocked(env, c.onceEnv)
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
