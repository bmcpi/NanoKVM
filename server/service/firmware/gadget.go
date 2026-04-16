package firmware

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
)

const (
	gadgetLUNPath   = "/sys/kernel/config/usb_gadget/g0/functions/mass_storage.disk0/lun.0"
	gadgetFilePath  = gadgetLUNPath + "/file"
	gadgetROPath    = gadgetLUNPath + "/ro"
	gadgetCdromPath = gadgetLUNPath + "/cdrom"
	gadgetInquiry   = gadgetLUNPath + "/inquiry_string"
	gadgetUDC       = "/sys/kernel/config/usb_gadget/g0/UDC"
)

// presentImage writes the loop device path to the USB mass storage gadget
// configfs and triggers a re-enumeration if needed.
// Uses the loop device (block device) rather than the image file so the kernel
// uses the device's native block size and writes are properly synchronized.
// Must be called with c.mu held.
func (c *Controller) presentImage() error {
	if c.presented {
		return nil
	}

	if c.loopDev == "" {
		return fmt.Errorf("no loop device available (image not mounted?)")
	}

	// Ensure not in cdrom or read-only mode.
	_ = os.WriteFile(gadgetCdromPath, []byte("0"), 0o666)
	_ = os.WriteFile(gadgetROPath, []byte("0"), 0o666)

	inquiry := fmt.Sprintf("%-8s%-16s%04x", "NanoKVM", "Firmware", 0x0100)
	_ = os.WriteFile(gadgetInquiry, []byte(inquiry), 0o666)

	// Present the loop device (block device), not the image file.
	// Per Linux docs, block device backing uses the device's native block
	// size and avoids the "BEWARE" about file-backed modification conflicts.
	_ = os.WriteFile(gadgetFilePath, []byte(""), 0o666)
	if err := os.WriteFile(gadgetFilePath, []byte(c.loopDev), 0o666); err != nil {
		return fmt.Errorf("write gadget file: %w", err)
	}

	// Only reset the UDC if it is not currently bound. If already bound,
	// the kernel picks up the LUN file change automatically.
	if !udcBound() {
		if err := resetUDC(); err != nil {
			return fmt.Errorf("reset UDC: %w", err)
		}
	}

	c.presented = true
	log.Infof("firmware: presented %s (loop %s) via USB gadget", c.imagePath, c.loopDev)
	return nil
}

// unpresentImage removes the image from the USB gadget. Must be called with c.mu held.
func (c *Controller) unpresentImage() error {
	if !c.presented {
		return nil
	}

	if err := os.WriteFile(gadgetFilePath, []byte(""), 0o666); err != nil {
		return fmt.Errorf("clear gadget file: %w", err)
	}

	c.presented = false
	log.Info("firmware: unpresented USB gadget")
	return nil
}

// Present presents the firmware image via USB gadget (public, acquires lock).
func (c *Controller) Present() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.presentImage()
}

// Unpresent removes the firmware image from the USB gadget (public, acquires lock).
func (c *Controller) Unpresent() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.unpresentImage()
}

func resetUDC() error {
	// Clear UDC.
	if err := os.WriteFile(gadgetUDC, []byte(""), 0o666); err != nil {
		return fmt.Errorf("clear UDC: %w", err)
	}

	time.Sleep(200 * time.Millisecond)

	// Re-assign UDC.
	cmd := exec.Command("sh", "-c", "ls /sys/class/udc/ | head -1")
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("list UDC: %w", err)
	}

	udc := strings.TrimSpace(string(out))
	if udc == "" {
		return fmt.Errorf("no UDC found")
	}

	if err := os.WriteFile(gadgetUDC, []byte(udc), 0o666); err != nil {
		return fmt.Errorf("write UDC: %w", err)
	}

	return nil
}

// udcBound returns true if the gadget UDC file contains a non-empty value,
// meaning a UDC controller is already bound.
func udcBound() bool {
	data, err := os.ReadFile(gadgetUDC)
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(data)) != ""
}
