// Package power provides centralized GPIO power management for the RPi 5.
//
// The GPIOPower pin directly controls whether the RPi 5 receives power:
//   - Pin state 1 = power on (RPi is receiving power)
//   - Pin state 0 = power off
//
// Turning on requires a toggle sequence (1→0→1) to trigger the boot process
// while leaving the pin high to maintain power. A simple set-to-1 without
// toggling won't initiate boot.
//
// All operations hold a write lock so concurrent GPIO state reads block until
// the operation completes, preventing LED flicker during multi-step toggles.
package power

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"

	"github.com/tinkerbell-community/NanoKVM/server/config"
)

// toggleDelay is the pause between GPIO state changes, giving the RPi 5
// time to register each edge transition.
const toggleDelay = 200 * time.Millisecond

// Controller manages RPi 5 power via the GPIOPower pin. All power operations
// are serialized via a read/write mutex so that State() calls block during
// multi-step sequences, preventing intermediate states from leaking to the UI.
type Controller struct {
	mu sync.RWMutex
}

var (
	instance *Controller
	once     sync.Once
)

// GetController returns the singleton power controller.
func GetController() *Controller {
	once.Do(func() {
		instance = &Controller{}
	})
	return instance
}

// State returns true if the managed system is powered on (GPIOPower pin = 1).
// Blocks during active power operations to report only the final state.
func (c *Controller) State() (bool, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.readPin()
}

// PowerOn turns the RPi 5 on. If already on, this is a no-op.
//
// From off (pin=0): toggle 1→0→1 to trigger boot, leaving power high.
func (c *Controller) PowerOn() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	on, err := c.readPin()
	if err != nil {
		return fmt.Errorf("read power state: %w", err)
	}
	if on {
		log.Debug("power: already on, no-op")
		return nil
	}

	log.Info("power: turning on (toggle 1→0→1)")
	return c.bootSequence()
}

// PowerOff turns the RPi 5 off by setting the power pin to 0.
// If already off, this is a no-op.
func (c *Controller) PowerOff() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	on, err := c.readPin()
	if err != nil {
		return fmt.Errorf("read power state: %w", err)
	}
	if !on {
		log.Debug("power: already off, no-op")
		return nil
	}

	log.Info("power: turning off (set pin to 0)")
	return c.writePin(0)
}

// Reset power-cycles the RPi 5. Turns off, then performs the boot toggle.
// If the system is already off, just performs power on.
func (c *Controller) Reset() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	on, err := c.readPin()
	if err != nil {
		return fmt.Errorf("read power state: %w", err)
	}

	if on {
		log.Info("power: reset — turning off first")
		if err := c.writePin(0); err != nil {
			return fmt.Errorf("reset power off: %w", err)
		}
		time.Sleep(toggleDelay)
	}

	log.Info("power: reset — boot sequence (1→0→1)")
	return c.bootSequence()
}

// ForceOff is an alias for PowerOff. With direct GPIO control there's no
// distinction between graceful and forced power off — setting pin to 0
// immediately cuts power.
func (c *Controller) ForceOff() error {
	return c.PowerOff()
}

// bootSequence performs the 1→0→1 toggle that triggers RPi 5 boot.
// Caller must hold c.mu write lock.
func (c *Controller) bootSequence() error {
	if err := c.writePin(1); err != nil {
		return fmt.Errorf("boot step 1 (set 1): %w", err)
	}
	time.Sleep(toggleDelay)

	if err := c.writePin(0); err != nil {
		return fmt.Errorf("boot step 2 (set 0): %w", err)
	}
	time.Sleep(toggleDelay)

	if err := c.writePin(1); err != nil {
		return fmt.Errorf("boot step 3 (set 1): %w", err)
	}
	return nil
}

// readPin reads the raw GPIOPower pin value. Returns true if pin=1 (on).
func (c *Controller) readPin() (bool, error) {
	path := config.GetInstance().Hardware.GPIOPower
	if path == "" {
		return false, fmt.Errorf("GPIOPower path not configured")
	}

	content, err := os.ReadFile(path)
	if err != nil {
		return false, fmt.Errorf("read %s: %w", path, err)
	}

	s := strings.TrimSpace(string(content))
	v, err := strconv.Atoi(s)
	if err != nil {
		return false, fmt.Errorf("parse gpio value %q: %w", s, err)
	}

	// Pin state 1 = on, 0 = off (active-high for RPi 5 power)
	return v == 1, nil
}

// writePin sets the GPIOPower pin to val (0 or 1).
func (c *Controller) writePin(val int) error {
	path := config.GetInstance().Hardware.GPIOPower
	if path == "" {
		return fmt.Errorf("GPIOPower path not configured")
	}

	data := []byte(strconv.Itoa(val))
	if err := os.WriteFile(path, data, 0o666); err != nil {
		return fmt.Errorf("write %s=%d: %w", path, val, err)
	}

	log.Debugf("power: pin %s = %d", path, val)
	return nil
}
