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
	// lun.0 — firmware image (U-Boot + env files).
	gadgetLUNPath   = "/sys/kernel/config/usb_gadget/g0/functions/mass_storage.disk0/lun.0"
	gadgetFilePath  = gadgetLUNPath + "/file"
	gadgetROPath    = gadgetLUNPath + "/ro"
	gadgetCdromPath = gadgetLUNPath + "/cdrom"
	gadgetInquiry   = gadgetLUNPath + "/inquiry_string"
	gadgetUDC       = "/sys/kernel/config/usb_gadget/g0/UDC"

	// lun.1 — virtual CD-ROM for ISO media (created on demand).
	gadgetLUN1Dir       = "/sys/kernel/config/usb_gadget/g0/functions/mass_storage.disk0/lun.1"
	gadgetLUN1File      = gadgetLUN1Dir + "/file"
	gadgetLUN1CDRom     = gadgetLUN1Dir + "/cdrom"
	gadgetLUN1RO        = gadgetLUN1Dir + "/ro"
	gadgetLUN1Removable = gadgetLUN1Dir + "/removable"
)

// presentImage writes the firmware image file path to the USB mass storage
// gadget configfs and triggers a re-enumeration if needed.
//
// The image file is presented directly (not a loop device). The host
// (U-Boot) boots from this image. The BMC accesses the env partition
// only through short-lived mount/unmount cycles, avoiding dual-access
// conflicts with the gadget's file-backed I/O.
// Must be called with c.mu held.
func (c *Controller) presentImage() error {
	if c.presented {
		return nil
	}

	// Ensure not in cdrom or read-only mode.
	_ = os.WriteFile(gadgetCdromPath, []byte("0"), 0o666)
	_ = os.WriteFile(gadgetROPath, []byte("0"), 0o666)

	inquiry := fmt.Sprintf("%-8s%-16s%04x", "NanoKVM", "Firmware", 0x0100)
	_ = os.WriteFile(gadgetInquiry, []byte(inquiry), 0o666)

	// Clear first, then set to the image file path. Retry on EBUSY because
	// a just-detached loop device or in-flight gadget I/O can briefly hold
	// the backing file.
	_ = os.WriteFile(gadgetFilePath, []byte("\n"), 0o666)
	var lastErr error
	for i := 0; i < 10; i++ {
		if err := os.WriteFile(gadgetFilePath, []byte(c.imagePath), 0o666); err == nil {
			lastErr = nil
			break
		} else {
			lastErr = err
			time.Sleep(100 * time.Millisecond)
		}
	}
	if lastErr != nil {
		return fmt.Errorf("write gadget file: %w", lastErr)
	}

	// Only reset the UDC if it is not currently bound. If already bound,
	// the kernel picks up the LUN file change automatically.
	if !udcBound() {
		if err := resetUDC(); err != nil {
			return fmt.Errorf("reset UDC: %w", err)
		}
	}

	c.presented = true
	log.Infof("firmware: presented %s via USB gadget", c.imagePath)
	return nil
}

// unpresentImage removes the image from the USB gadget. Must be called with c.mu held.
// After this returns, the image file is no longer held by the kernel's
// f_mass_storage and is safe to mount via loop device.
func (c *Controller) unpresentImage() error {
	if !c.presented {
		return nil
	}

	if err := os.WriteFile(gadgetFilePath, []byte("\n"), 0o666); err != nil {
		return fmt.Errorf("clear gadget file: %w", err)
	}

	// Give the kernel a moment to drop its hold on the backing file.
	time.Sleep(100 * time.Millisecond)

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
	if err := os.WriteFile(gadgetUDC, []byte("\n"), 0o666); err != nil {
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

// ---- virtual media (lun.1 CD-ROM) -----------------------------------------

// ensureLUN1 creates the lun.1 directory in configfs and configures it as a
// read-only, removable CD-ROM. If the UDC is already bound (e.g. from a
// previous server run — configfs persists across process restarts), it unbinds,
// creates lun.1, then rebinds. Must be called with c.mu held.
func (c *Controller) ensureLUN1() error {
	if _, err := os.Stat(gadgetLUN1Dir); err == nil {
		return nil // already exists from a previous run
	}

	if udcBound() {
		// Unbind the UDC and wait until the kernel confirms it is released.
		// Writing "" is asynchronous on some kernels; poll until clear.
		_ = os.WriteFile(gadgetUDC, []byte("\n"), 0o666)
		for i := 0; i < 20; i++ {
			time.Sleep(50 * time.Millisecond)
			if !udcBound() {
				break
			}
		}
		if udcBound() {
			return fmt.Errorf("timed out waiting for UDC to unbind before creating lun.1")
		}
		// Also clear lun.0/file so f_mass_storage releases its hold.
		_ = os.WriteFile(gadgetFilePath, []byte("\n"), 0o666)
		time.Sleep(50 * time.Millisecond)
	}

	if err := os.Mkdir(gadgetLUN1Dir, 0o755); err != nil {
		return fmt.Errorf("create lun.1: %w", err)
	}
	// cdrom=1 → 2048-byte El Torito mode. UEFI/BIOS targets boot via El Torito
	// (not through a partition filesystem), so no FAT sector-size mismatch arises.
	_ = os.WriteFile(gadgetLUN1CDRom, []byte("0"), 0o666)
	_ = os.WriteFile(gadgetLUN1RO, []byte("1"), 0o666)
	_ = os.WriteFile(gadgetLUN1Removable, []byte("1"), 0o666)

	// Rebind so the host enumerates both LUNs.
	if err := resetUDC(); err != nil {
		return fmt.Errorf("rebind UDC after lun.1 create: %w", err)
	}
	// presentImage will re-write lun.0/file; mark as not presented so it does.
	c.presented = false

	log.Info("firmware: virtual media LUN (lun.1) created and UDC rebound")
	return nil
}

// presentISO sets the ISO file as the backing file for lun.1. The host sees
// a USB CD-ROM containing the ISO. Must be called with c.mu held.
func (c *Controller) presentISO(isoPath string) error {
	if err := c.ensureLUN1(); err != nil {
		return err
	}
	// Ensure attributes are set (configfs survives a process restart but
	// may not persist across reboots depending on the init scripts).
	_ = os.WriteFile(gadgetLUN1CDRom, []byte("0"), 0o666)
	_ = os.WriteFile(gadgetLUN1RO, []byte("1"), 0o666)
	_ = os.WriteFile(gadgetLUN1Removable, []byte("1"), 0o666)

	if err := os.WriteFile(gadgetLUN1File, []byte(isoPath), 0o666); err != nil {
		return fmt.Errorf("set lun.1 file: %w", err)
	}
	log.Infof("firmware: virtual media lun.1 → %s", isoPath)
	return nil
}

// unpresentISO clears the backing file from lun.1 so the host sees an empty
// CD-ROM tray. Must be called with c.mu held.
func (c *Controller) unpresentISO() error {
	if _, err := os.Stat(gadgetLUN1Dir); err != nil {
		return nil // lun.1 doesn't exist; nothing to clear
	}
	if err := os.WriteFile(gadgetLUN1File, []byte("\n"), 0o666); err != nil {
		return fmt.Errorf("clear lun.1 file: %w", err)
	}
	log.Info("firmware: virtual media lun.1 cleared")
	return nil
}
