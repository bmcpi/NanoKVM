package sysctl

import (
	"os"
	"strconv"
	"strings"
)

// MarkOLED creates or removes the OLED marker file.
// Creates /etc/kvm/oled_exist if present, removes it if not.
func MarkOLED(exists bool) error {
	if exists {
		f, err := os.Create("/etc/kvm/oled_exist")
		if err != nil {
			return err
		}
		return f.Close()
	}
	os.Remove("/etc/kvm/oled_exist")
	return nil
}

// GetPingAllowed checks if pinging is allowed (no /etc/kvm/stop_ping file).
func GetPingAllowed() bool {
	_, err := os.Stat("/etc/kvm/stop_ping")
	return os.IsNotExist(err)
}

// GetOLEDSleepTimeout reads the OLED sleep timeout from /etc/kvm/oled_sleep.
// Returns 0 if file doesn't exist (sleep disabled), or the timeout in seconds.
// Defaults to 30 on parse error.
func GetOLEDSleepTimeout() uint16 {
	data, err := os.ReadFile("/etc/kvm/oled_sleep")
	if err != nil {
		return 0
	}
	val, err := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 16)
	if err != nil {
		return 30
	}
	return uint16(val)
}

// ReadUSBState reads the USB device controller state.
// Returns: 0 = not attached, 1 = configured, -1 = unknown.
func ReadUSBState() int8 {
	data, err := os.ReadFile("/sys/class/udc/4340000.usb/state")
	if err != nil {
		return -1
	}
	state := strings.TrimSpace(string(data))
	switch {
	case strings.HasPrefix(state, "n"): // "not attached"
		return 0
	case strings.HasPrefix(state, "c"): // "configured"
		return 1
	default:
		return -1
	}
}

// ReadHostPowerState reads the host power GPIO pin.
// Returns 1 if the pin is high (power on), 0 if low (power off), or -1 on error.
func ReadHostPowerState(gpioPath string) int8 {
	data, err := os.ReadFile(gpioPath)
	if err != nil {
		return -1
	}
	if strings.TrimSpace(string(data)) == "1" {
		return 1
	}
	return 0
}
