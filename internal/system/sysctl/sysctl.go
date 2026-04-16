package sysctl

import (
	"os"
	"strings"
)

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
