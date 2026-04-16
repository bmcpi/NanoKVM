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
	envFile    string

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
			imageURL:   cfg.Firmware.ImageURL,
			imagePath:  cfg.Firmware.ImagePath,
			mountPoint: cfg.Firmware.MountPoint,
			envFile:    cfg.Firmware.EnvFile,
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

// LoadEnv reads and parses the U-Boot environment file.
func (c *Controller) LoadEnv() (*ubootenv.Env, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	var env *ubootenv.Env
	err := c.withMount(func() error {
		var e error
		env, e = ubootenv.LoadFile(c.envFile)
		return e
	})
	return env, err
}

// SaveEnv serializes and writes the U-Boot environment file atomically.
func (c *Controller) SaveEnv(env *ubootenv.Env) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.withMount(func() error {
		return env.SaveFile(c.envFile)
	})
}

// GetBootTarget reads the current boot target from the U-Boot environment.
// Returns the raw boot_targets string (e.g. "mmc0 usb0 pxe dhcp").
func (c *Controller) GetBootTarget() (string, error) {
	env, err := c.LoadEnv()
	if err != nil {
		return "", err
	}
	v, _ := env.Get(ubootenv.VarBootTargets)
	return v, nil
}

// SetBootTarget writes a boot target string to the U-Boot environment.
func (c *Controller) SetBootTarget(targets string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.withMount(func() error {
		env, err := ubootenv.LoadFile(c.envFile)
		if err != nil {
			return fmt.Errorf("load env: %w", err)
		}

		if targets == "" {
			env.Delete(ubootenv.VarBootTargets)
		} else {
			env.Set(ubootenv.VarBootTargets, targets)
		}

		return env.SaveFile(c.envFile)
	})
}

// GetInventory returns board inventory data from the U-Boot environment.
func (c *Controller) GetInventory() (map[string]string, error) {
	env, err := c.LoadEnv()
	if err != nil {
		return nil, err
	}
	return env.GetInventory(), nil
}

// GetAllEnvVars returns all U-Boot environment variables.
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
