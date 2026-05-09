package firmware

// uboot_env.go provides accessors for the binary U-Boot env partition file
// (uboot.env) inside the firmware FAT image.
//
// The file is U-Boot's native on-disk environment (4-byte CRC32 LE header
// followed by NUL-terminated key=value entries, padded to a fixed size),
// distinct from the plain-text machine.env / persistent.env / once.env
// preboot files. It is read like machine.env (effective values U-Boot
// reads at boot) and written like persistent.env (atomic save through a
// short-lived mount cycle).

import (
	"errors"
	"fmt"
	"os"

	log "github.com/sirupsen/logrus"

	"github.com/BMCPi/NanoKVM/server/service/ubootenv"
)

// loadUbootEnvFresh reads and parses uboot.env from the image without
// mounting. Returns an empty binary-format Env when the file is missing.
// Must hold c.mu.
func (c *Controller) loadUbootEnvFresh() (*ubootenv.Env, error) {
	if c.ubootEnv == "" {
		return ubootenv.NewBinary(0), nil
	}
	data, err := c.readFileFresh(c.fatRelPath(c.ubootEnv))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ubootenv.NewBinary(0), nil
		}
		return nil, err
	}
	env, err := ubootenv.Parse(data)
	if err != nil {
		return nil, err
	}
	// Promote a text-parsed env to binary if we expected binary (the file
	// is named uboot.env and U-Boot only consumes the binary format).
	if env.Format != ubootenv.FormatBinary {
		env.Format = ubootenv.FormatBinary
		if env.Size == 0 {
			env.Size = ubootenv.DefaultEnvSize
		}
	}
	return env, nil
}

// loadUbootEnvForWrite loads uboot.env from the (mounted) image as a binary
// env, defaulting to a fresh empty binary env if the file is missing.
// Must be called inside withMount().
func loadUbootEnvForWrite(path string) (*ubootenv.Env, error) {
	env, err := ubootenv.LoadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ubootenv.NewBinary(0), nil
		}
		return nil, err
	}
	if env.Format != ubootenv.FormatBinary {
		env.Format = ubootenv.FormatBinary
		if env.Size == 0 {
			env.Size = ubootenv.DefaultEnvSize
		}
	}
	return env, nil
}

// LoadUbootEnv returns the parsed uboot.env. Fresh read.
func (c *Controller) LoadUbootEnv() (*ubootenv.Env, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.loadUbootEnvFresh()
}

// GetUbootEnvVar returns a single variable from uboot.env. Fresh read.
func (c *Controller) GetUbootEnvVar(key string) (string, bool, error) {
	env, err := c.LoadUbootEnv()
	if err != nil {
		return "", false, err
	}
	v, ok := env.Get(key)
	return v, ok, nil
}

// SetUbootEnvVar writes a variable to uboot.env, preserving the binary
// format. Pass an empty value with del=true to delete the key.
func (c *Controller) SetUbootEnvVar(key, value string, del bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	defer c.invalidateReaderCacheLocked()

	if c.ubootEnv == "" {
		return fmt.Errorf("ubootEnv path not configured")
	}

	return c.withMount(func() error {
		env, err := loadUbootEnvForWrite(c.ubootEnv)
		if err != nil {
			return fmt.Errorf("load uboot env: %w", err)
		}
		if del {
			env.Delete(key)
		} else {
			env.Set(key, value)
		}
		return env.SaveFile(c.ubootEnv)
	})
}

// SetUbootEnvVars applies multiple variable updates to uboot.env atomically
// (single mount/save). Entries with empty value+del=true are deleted.
func (c *Controller) SetUbootEnvVars(updates map[string]string, deletes []string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	defer c.invalidateReaderCacheLocked()

	if c.ubootEnv == "" {
		return fmt.Errorf("ubootEnv path not configured")
	}

	return c.withMount(func() error {
		env, err := loadUbootEnvForWrite(c.ubootEnv)
		if err != nil {
			return fmt.Errorf("load uboot env: %w", err)
		}
		for k, v := range updates {
			env.Set(k, v)
		}
		for _, k := range deletes {
			env.Delete(k)
		}
		return env.SaveFile(c.ubootEnv)
	})
}

// ensureUbootEnvLocked creates an empty default uboot.env in the image if
// the file is missing. Idempotent. Must hold c.mu.
func (c *Controller) ensureUbootEnvLocked() error {
	if c.ubootEnv == "" {
		return nil
	}
	// Fast path: if uboot.env is already present in the image, do nothing.
	if _, err := c.readFileFresh(c.fatRelPath(c.ubootEnv)); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("probe uboot.env: %w", err)
	}

	defer c.invalidateReaderCacheLocked()
	return c.withMount(func() error {
		// Re-check inside the mount in case the userspace reader cache
		// was stale; if it now exists, we're done.
		if _, err := os.Stat(c.ubootEnv); err == nil {
			return nil
		}
		env := ubootenv.NewBinary(0)
		if err := env.SaveFile(c.ubootEnv); err != nil {
			return fmt.Errorf("create default uboot.env: %w", err)
		}
		log.Infof("firmware: created default empty uboot.env (%d bytes)", env.Size)
		return nil
	})
}
