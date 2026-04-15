// Package oledui renders pages on the SSD1306 OLED display for the
// kvm_system daemon. It is a Go port of the C++ oled_ui module from
// support/sg2002/kvm_system/main/lib/oled_ui/oled_ui.cpp.
package oledui

import (
	"github.com/tinkerbell-community/NanoKVM/internal/system/sysctl"
)

const (
	// sleepDelayMin is the minimum acceptable timeout in seconds.
	// Values below this threshold disable auto-sleep entirely,
	// matching OLED_SLEEP_DELAY_MIN in the C++ codebase.
	sleepDelayMin uint16 = 10
)

// resetSleepTimer resets the inactivity counter so the display stays awake.
func (u *UI) resetSleepTimer() {
	u.sleepTimer = 0
}

// checkSleep reads the configured OLED sleep timeout and decides whether the
// display should be put to sleep. It mirrors the oled_auto_sleep() function
// in the C++ source.
//
// When the timeout is 0 or below the minimum threshold, sleep is disabled.
// Otherwise the timer is compared against the timeout (in seconds). Because
// Update() is called at ~1 Hz, sleepTimer directly counts elapsed seconds.
//
// Returns true when the display has just transitioned to the sleeping state.
func (u *UI) checkSleep() bool {
	timeout := sysctl.GetOLEDSleepTimeout()

	if timeout < sleepDelayMin {
		// Sleep disabled – if we were sleeping, wake up.
		if u.sleeping {
			u.sleeping = false
			u.oled.DisplayOn()
		}
		return false
	}

	if u.sleeping {
		return false
	}

	u.sleepTimer++
	if uint16(u.sleepTimer) >= timeout {
		u.sleeping = true
		u.oled.DisplayOff()
		return true
	}
	return false
}
