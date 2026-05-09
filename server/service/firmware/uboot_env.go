package firmware

// uboot_env.go provides read-only accessors for machine.env — the plain-text
// U-Boot environment file written by U-Boot at each boot. It is the
// authoritative source of the current effective environment.
//
// persistent.env and once.env are the write-side files managed by firmware.go.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	log "github.com/sirupsen/logrus"

	"github.com/BMCPi/NanoKVM/server/service/ubootenv"
)

// LoadUbootEnv returns the parsed machine.env. Fresh read.
func (c *Controller) LoadUbootEnv() (*ubootenv.Env, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.loadEnvFresh(c.machineEnv)
}

// GetUbootEnvVar returns a single variable from machine.env. Fresh read.
func (c *Controller) GetUbootEnvVar(key string) (string, bool, error) {
	env, err := c.LoadUbootEnv()
	if err != nil {
		return "", false, err
	}
	v, ok := env.Get(key)
	return v, ok, nil
}

// SetUbootEnvVar writes a variable to persistent.env inside the firmware image,
// preserving existing variables. Pass del=true to delete the key instead.
func (c *Controller) SetUbootEnvVar(key, value string, del bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	defer c.invalidateReaderCacheLocked()

	if c.persistentEnv == "" {
		return fmt.Errorf("persistentEnv path not configured")
	}

	return c.withMount(func() error {
		dest := filepath.Join(c.mountPoint, filepath.Base(c.persistentEnv))
		env, err := loadEnvFile(dest)
		if err != nil {
			return fmt.Errorf("load persistent env: %w", err)
		}
		if del {
			env.Delete(key)
		} else {
			env.Set(key, value)
		}
		return saveOrRemoveEnv(env, dest)
	})
}

// SetUbootEnvVars applies multiple variable updates to persistent.env inside
// the firmware image atomically (single mount/save).
func (c *Controller) SetUbootEnvVars(updates map[string]string, deletes []string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	defer c.invalidateReaderCacheLocked()

	if c.persistentEnv == "" {
		return fmt.Errorf("persistentEnv path not configured")
	}

	return c.withMount(func() error {
		dest := filepath.Join(c.mountPoint, filepath.Base(c.persistentEnv))
		env, err := loadEnvFile(dest)
		if err != nil {
			return fmt.Errorf("load persistent env: %w", err)
		}
		for k, v := range updates {
			env.Set(k, v)
		}
		for _, k := range deletes {
			env.Delete(k)
		}
		return saveOrRemoveEnv(env, dest)
	})
}

// ensureUbootEnvLocked creates an empty default machine.env in the image if
// the file is missing, so U-Boot has somewhere to write saveenv output.
// Idempotent. Must hold c.mu.
func (c *Controller) ensureUbootEnvLocked() error {
	if c.machineEnv == "" {
		return nil
	}
	// Fast path: already present.
	if _, err := c.readFileFresh(c.fatRelPath(c.machineEnv)); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("probe machine.env: %w", err)
	}

	defer c.invalidateReaderCacheLocked()
	return c.withMount(func() error {
		// Use the mounted-image path, not the host firmwareDir path.
		dest := filepath.Join(c.mountPoint, filepath.Base(c.machineEnv))
		if _, err := os.Stat(dest); err == nil {
			return nil
		}
		env := ubootenv.New()
		if err := env.SaveFile(dest); err != nil {
			return fmt.Errorf("create default machine.env: %w", err)
		}
		log.Infof("firmware: created default empty machine.env")
		return nil
	})
}
