// Package power provides GPIO power management for the BMC.
//
// # Button-press mode (preferred)
//
// When the power-LED GPIO (GPIOPowerLED) is configured and readable, the
// controller operates in button-press mode:
//   - Power state is read from the LED pin (1 = on, 0 = off).
//   - The power-button GPIO (GPIOPower) is normally held HIGH (not pressed).
//   - A press is simulated by pulling the pin LOW for a duration, then
//     releasing back to HIGH.
//   - Short press (~300 ms): power-on or graceful/soft shutdown.
//   - Long press  (≥5 s):   forced hard shutdown (ATX-style hold).
//
// # Legacy / fallback mode
//
// When GPIOPowerLED is not configured or unreadable the controller falls back
// to direct GPIO control (original behaviour):
//   - State is read from the GPIOPower pin itself (1 = on, 0 = off).
//   - Power-on uses a 1→0→1 toggle sequence to trigger boot.
//   - Power-off / force-off set the pin directly to 0 (immediate cut).
//
// All public methods are serialized via a mutex.
package power

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"

	"github.com/BMCPi/NanoKVM/server/config"
)

const (
	// shortPressDuration is the hold time for power-on and graceful shutdown.
	shortPressDuration = 300 * time.Millisecond

	// longPressDuration is the hold time for a forced power-off (ATX spec ≥4 s).
	longPressDuration = 5500 * time.Millisecond

	// toggleDelay is the pause between steps in the legacy boot sequence.
	toggleDelay = 200 * time.Millisecond

	// postPressDelay lets the hardware settle after releasing the button.
	postPressDelay = 500 * time.Millisecond

	// offPollInterval / offPollTimeout: how often / how long waitForOff polls.
	offPollInterval = 250 * time.Millisecond
	offPollTimeout  = 30 * time.Second
)

// Controller manages system power via GPIO. All public methods are serialized
// via a mutex to prevent concurrent GPIO sequences.
type Controller struct {
	mu sync.Mutex
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

// ── Public API ────────────────────────────────────────────────────────────────

// State reports whether the system is powered on.
// Reads the power-LED GPIO if available; otherwise reads the button pin directly.
func (c *Controller) State() (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.readState()
}

// PowerOn powers on the system.
//
// Button mode:  sends a short press if the system is currently off.
// Legacy mode:  runs the 1→0→1 boot sequence on the power pin.
func (c *Controller) PowerOn() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	on, err := c.readState()
	if err != nil {
		return fmt.Errorf("read state: %w", err)
	}
	if on {
		log.Debug("power: already on, no-op")
		return nil
	}

	if !isLegacy() {
		log.Info("power: power-on (short button press)")
		return c.buttonPress(shortPressDuration)
	}

	log.Info("power: power-on boot sequence (legacy mode)")
	return c.legacyBootSequence()
}

// PowerOff sends a graceful/soft shutdown signal.
//
// Button mode:  short button press (OS handles the ACPI shutdown).
// Legacy mode:  sets the power pin to 0 (immediate cut).
func (c *Controller) PowerOff() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	on, err := c.readState()
	if err != nil {
		return fmt.Errorf("read state: %w", err)
	}
	if !on {
		log.Debug("power: already off, no-op")
		return nil
	}

	if !isLegacy() {
		log.Info("power: soft shutdown (short button press)")
		return c.buttonPress(shortPressDuration)
	}

	log.Info("power: power-off (legacy — set pin 0)")
	return c.legacyWritePin(0)
}

// ForceOff forces an immediate hard shutdown.
//
// Button mode:  holds the power button LOW for ≥5 s.
// Legacy mode:  sets the power pin to 0 (same as PowerOff — immediate cut).
func (c *Controller) ForceOff() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	on, err := c.readState()
	if err != nil {
		return fmt.Errorf("read state: %w", err)
	}
	if !on {
		log.Debug("power: already off, no-op")
		return nil
	}

	if !isLegacy() {
		log.Info("power: force shutdown (long button press ≥5 s)")
		return c.buttonPress(longPressDuration)
	}

	log.Info("power: force-off (legacy — set pin 0)")
	return c.legacyWritePin(0)
}

// Reset forces the system off and powers it back on.
func (c *Controller) Reset() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	on, err := c.readState()
	if err != nil {
		return fmt.Errorf("read state: %w", err)
	}

	if on {
		if !isLegacy() {
			log.Info("power: reset — force off (long button press)")
			if err := c.buttonPress(longPressDuration); err != nil {
				return fmt.Errorf("reset force-off: %w", err)
			}
			if err := c.waitForOff(); err != nil {
				log.Warnf("power: reset — timed out waiting for off, proceeding anyway: %v", err)
			}
		} else {
			log.Info("power: reset — off (legacy)")
			if err := c.legacyWritePin(0); err != nil {
				return fmt.Errorf("reset legacy off: %w", err)
			}
			time.Sleep(toggleDelay)
		}
	}

	if !isLegacy() {
		log.Info("power: reset — power on (short press)")
		return c.buttonPress(shortPressDuration)
	}

	log.Info("power: reset — boot sequence (legacy)")
	return c.legacyBootSequence()
}

// ── Internal helpers ──────────────────────────────────────────────────────────

// isLegacy reports whether legacy direct-GPIO mode is enabled via config.
func isLegacy() bool {
	return config.GetInstance().Power.LegacyMode
}

// readState returns true if the system is powered on.
// Button mode: reads the power-LED GPIO.
// Legacy mode:  reads the button/power pin directly.
// Caller must hold c.mu.
func (c *Controller) readState() (bool, error) {
	hw := config.GetInstance().Hardware

	if !isLegacy() {
		if hw.GPIOPowerLED == "" {
			return false, fmt.Errorf("GPIOPowerLED not configured (required for button-press mode)")
		}
		v, err := readGPIO(hw.GPIOPowerLED)
		if err != nil {
			return false, fmt.Errorf("read power-LED pin: %w", err)
		}
		return v == 1, nil
	}

	v, err := readGPIO(hw.GPIOPower)
	if err != nil {
		return false, fmt.Errorf("read power pin: %w", err)
	}
	return v == 1, nil
}

// buttonPress simulates a button press: ensures the pin is HIGH, pulls it LOW
// for duration, then restores HIGH. The pin is always left at 1 (released).
// Caller must hold c.mu.
func (c *Controller) buttonPress(duration time.Duration) error {
	path := config.GetInstance().Hardware.GPIOPower
	if path == "" {
		return fmt.Errorf("GPIOPower not configured")
	}

	// Guarantee starting state is released (high).
	if err := writeGPIO(path, 1); err != nil {
		return fmt.Errorf("pre-press release: %w", err)
	}

	// Pull low — button pressed.
	if err := writeGPIO(path, 0); err != nil {
		return fmt.Errorf("press down: %w", err)
	}
	time.Sleep(duration)

	// Restore high — button released.
	if err := writeGPIO(path, 1); err != nil {
		return fmt.Errorf("press release: %w", err)
	}

	time.Sleep(postPressDelay)
	return nil
}

// waitForOff polls until the power LED reports off, or until offPollTimeout.
// Caller must hold c.mu.
func (c *Controller) waitForOff() error {
	deadline := time.Now().Add(offPollTimeout)
	for time.Now().Before(deadline) {
		on, err := c.readState()
		if err == nil && !on {
			return nil
		}
		time.Sleep(offPollInterval)
	}
	return fmt.Errorf("timed out after %s waiting for power off", offPollTimeout)
}

// ── Legacy helpers ────────────────────────────────────────────────────────────

// legacyBootSequence performs the 1→0→1 toggle that triggers boot when the
// power pin is wired directly to the power supply enable.
// Caller must hold c.mu.
func (c *Controller) legacyBootSequence() error {
	path := config.GetInstance().Hardware.GPIOPower
	for _, v := range []int{1, 0, 1} {
		if err := writeGPIO(path, v); err != nil {
			return fmt.Errorf("boot sequence (write %d): %w", v, err)
		}
		time.Sleep(toggleDelay)
	}
	return nil
}

// legacyWritePin sets the power pin directly (0 = off, 1 = on).
// Caller must hold c.mu.
func (c *Controller) legacyWritePin(val int) error {
	path := config.GetInstance().Hardware.GPIOPower
	if err := writeGPIO(path, val); err != nil {
		return fmt.Errorf("write power pin %d: %w", val, err)
	}
	return nil
}

// ── Low-level sysfs GPIO access ───────────────────────────────────────────────

func readGPIO(path string) (int, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	if strings.TrimSpace(string(raw)) == "1" {
		return 1, nil
	}
	return 0, nil
}

func writeGPIO(path string, val int) error {
	v := "0"
	if val != 0 {
		v = "1"
	}
	if err := os.WriteFile(path, []byte(v), 0o644); err != nil {
		return fmt.Errorf("write %s=%s: %w", path, v, err)
	}
	log.Debugf("power: gpio %s = %s", path, v)
	return nil
}
