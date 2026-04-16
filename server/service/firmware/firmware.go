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
	Mounted    bool   `json:"mounted"`
	Presented  bool   `json:"presented"`
	EnvReady   bool   `json:"envReady"`
	ImagePath  string `json:"imagePath"`
	MountPoint string `json:"mountPoint"`
}

// Controller manages the firmware image lifecycle.
type Controller struct {
	mu sync.RWMutex

	imageURL   string
	imagePath  string
	mountPoint string
	envFile    string

	mounted   bool
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

// Init checks whether the firmware image is already available and attempts
// to mount/present it. Call once at server startup.
func (c *Controller) Init() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.imageExists() {
		log.Info("firmware: image not found at ", c.imagePath)
		return nil
	}

	log.Info("firmware: image found, mounting")
	if err := c.mountImage(); err != nil {
		return fmt.Errorf("firmware init mount: %w", err)
	}

	log.Info("firmware: presenting via USB gadget")
	if err := c.presentImage(); err != nil {
		log.Warnf("firmware: USB gadget present failed (may not be available in this environment): %v", err)
	}

	return nil
}

// GetStatus returns the current lifecycle state.
func (c *Controller) GetStatus() Status {
	c.mu.RLock()
	defer c.mu.RUnlock()

	envReady := false
	if c.mounted {
		if _, err := os.Stat(c.envFile); err == nil {
			envReady = true
		}
	}

	return Status{
		Downloaded: c.imageExists(),
		Mounted:    c.mounted,
		Presented:  c.presented,
		EnvReady:   envReady,
		ImagePath:  c.imagePath,
		MountPoint: c.mountPoint,
	}
}

// LoadEnv reads and parses the U-Boot environment file.
func (c *Controller) LoadEnv() (*ubootenv.Env, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if !c.mounted {
		return nil, fmt.Errorf("firmware image not mounted")
	}

	return ubootenv.LoadFile(c.envFile)
}

// SaveEnv serializes and writes the U-Boot environment file atomically.
func (c *Controller) SaveEnv(env *ubootenv.Env) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.mounted {
		return fmt.Errorf("firmware image not mounted")
	}

	return env.SaveFile(c.envFile)
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

	if !c.mounted {
		return fmt.Errorf("firmware image not mounted")
	}

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
