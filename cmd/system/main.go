// kvm_system is the NanoKVM board management daemon.
//
// It drives the SSD1306 OLED display, handles physical button input, monitors
// network interfaces, and manages the WiFi AP configuration flow. This is a
// Go port of support/sg2002/kvm_system/main/src/main.cpp.
package main

import (
	"context"
	"os/signal"
	"syscall"
	"time"

	log "github.com/sirupsen/logrus"

	"github.com/tinkerbell-community/NanoKVM/internal/system/button"
	"github.com/tinkerbell-community/NanoKVM/internal/system/netmon"
	"github.com/tinkerbell-community/NanoKVM/internal/system/oled"
	"github.com/tinkerbell-community/NanoKVM/internal/system/oledui"
	"github.com/tinkerbell-community/NanoKVM/internal/system/sysctl"
	"github.com/tinkerbell-community/NanoKVM/internal/system/wificonfig"
	"github.com/tinkerbell-community/NanoKVM/server/config"
)

func main() {
	log.SetFormatter(&log.TextFormatter{FullTimestamp: true})

	ctx, cancel := signal.NotifyContext(context.Background(),
		syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// Resolve hardware-specific GPIO paths.
	hw := config.GetInstance().Hardware
	log.Infof("hardware version: %s", hw.Version)

	// Initialize OLED display.
	display, err := oled.New()
	if err != nil {
		log.WithError(err).Warn("OLED init failed, continuing without display")
	}
	defer func() {
		if display != nil {
			display.Close()
		}
	}()

	oledPresent := display != nil && display.Exists()
	if err := sysctl.MarkOLED(oledPresent); err != nil {
		log.WithError(err).Warn("failed to mark OLED state")
	}

	if oledPresent {
		display.Init()
		display.ShowKVMLogo()
		time.Sleep(300 * time.Millisecond)
		display.Clear()
	}

	// Start subsystems.
	netMon := netmon.NewMonitor()
	go netMon.Run(ctx)

	wifiCfg := wificonfig.New()

	btnHandler := button.New()
	go func() {
		if err := btnHandler.Run(ctx); err != nil {
			log.WithError(err).Error("button handler exited")
		}
	}()

	ui := oledui.New(display, netMon, wifiCfg, hw.GPIOPower)

	// Main loop: ~1 Hz tick matching the C++ STATE_DELAY.
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	log.Info("kvm_system daemon started")

	for {
		select {
		case <-ctx.Done():
			log.Info("shutting down")
			if oledPresent {
				display.Clear()
				display.DisplayOff()
			}
			return

		case evt := <-btnHandler.Events():
			handleButtonEvent(ui, wifiCfg, evt)

		case <-ticker.C:
			if oledPresent {
				ui.Update()
			}
			if ui.GetPage() == oledui.PageWiFiCfg {
				wifiCfg.Process()
			}
		}
	}
}

func handleButtonEvent(ui *oledui.UI, wifiCfg *wificonfig.WiFiConfig, evt button.Event) {
	ui.Wake()

	switch evt {
	case button.EventShortPress:
		ui.ToggleSubPage()

	case button.EventLongPress:
		if ui.GetPage() == oledui.PageMain {
			ui.SetPage(oledui.PageWiFiCfg)
			if err := wifiCfg.Start(); err != nil {
				log.WithError(err).Error("WiFi config start failed")
			}
		} else {
			ui.SetPage(oledui.PageMain)
			if err := wificonfig.RestartWiFi(); err != nil {
				log.WithError(err).Error("WiFi restart failed")
			}
		}

	case button.EventVeryLongPress:
		log.Info("password reset triggered by very long press")
		if err := wificonfig.ResetPassword(); err != nil {
			log.WithError(err).Error("password reset failed")
		}
	}
}
