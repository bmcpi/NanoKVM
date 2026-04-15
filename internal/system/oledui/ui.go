package oledui

import (
	"fmt"

	"github.com/tinkerbell-community/NanoKVM/internal/system/netmon"
	"github.com/tinkerbell-community/NanoKVM/internal/system/oled"
	"github.com/tinkerbell-community/NanoKVM/internal/system/sysctl"
	"github.com/tinkerbell-community/NanoKVM/internal/system/wificonfig"

	qrcode "github.com/skip2/go-qrcode"
)

// Page selects which top-level OLED page to display.
type Page int

const (
	PageMain    Page = 0 // network / power / branding
	PageWiFiCfg Page = 1 // WiFi AP configuration flow
)

const (
	// ipSwapInterval is the number of Update() ticks (≈1 Hz) between
	// alternating the ETH and WiFi IP display. Matches the C++ constant
	// IP_Change_time / STATE_DELAY = 5000 / 1000 = 5.
	ipSwapInterval = 5

	// qrSwapInterval is the number of Update() ticks between swapping
	// the WiFi config page between QR code and text views.
	// Matches QR_Change_time / STATE_DELAY = 5000 / 1000 = 5.
	qrSwapInterval = 5

	// Icon indices into oled.KvmIcons.
	iconETHOff  uint8 = 4
	iconETHOn   uint8 = 5
	iconWiFiOff uint8 = 6
	iconWiFiOn  uint8 = 7

	// Icon widths in pixels.
	iconSize uint8 = 15
)

// UI drives the OLED display pages. Create with New() and call Update() at
// approximately 1 Hz.
type UI struct {
	oled    *oled.OLED
	netMon  *netmon.Monitor
	wifiCfg *wificonfig.WiFiConfig

	page    Page
	subPage int // sub-page within current page

	sleepTimer int  // counts up each Update(), compared to timeout
	sleeping   bool // true while display is off due to inactivity

	// IP rotation state.
	ipSwapTimer int
	showEthIP   bool // when true show ETH IP, else WiFi IP

	// WiFi config page alternates between QR and text views.
	qrSwapTimer int
	showQR      bool

	// GPIO sysfs value path for the host power LED.
	powerGPIOPath string

	// Tracks whether the current page/subPage has been drawn at least once.
	firstDraw bool

	// Previous network state used for dirty detection.
	prevEthIP  string
	prevWiFiIP string
}

// New creates a UI bound to the provided OLED driver, network monitor,
// WiFi config state machine, and host-power GPIO path.
func New(o *oled.OLED, nm *netmon.Monitor, wc *wificonfig.WiFiConfig, powerGPIOPath string) *UI {
	return &UI{
		oled:          o,
		netMon:        nm,
		wifiCfg:       wc,
		powerGPIOPath: powerGPIOPath,
		showEthIP:     true,
		showQR:        true,
		firstDraw:     true,
	}
}

// SetPage switches to the given page and resets the sub-page index.
func (u *UI) SetPage(p Page) {
	if u.page != p {
		u.page = p
		u.subPage = 0
		u.firstDraw = true
	}
}

// GetPage returns the active page.
func (u *UI) GetPage() Page {
	return u.page
}

// ToggleSubPage cycles through sub-pages within the current page.
func (u *UI) ToggleSubPage() {
	switch u.page {
	case PageMain:
		u.subPage = (u.subPage + 1) % 2
	case PageWiFiCfg:
		u.subPage = (u.subPage + 1) % 6
	}
	u.firstDraw = true
}

// Wake resets the sleep timer and turns the display back on if it was sleeping.
func (u *UI) Wake() {
	u.resetSleepTimer()
	if u.sleeping {
		u.sleeping = false
		u.oled.DisplayOn()
		u.firstDraw = true
	}
}

// CheckSleep returns true when the display is currently sleeping.
func (u *UI) CheckSleep() bool {
	return u.sleeping
}

// Update renders the current page. It should be called at ~1 Hz.
func (u *UI) Update() {
	// Handle auto-sleep on the main page.
	if u.page == PageMain {
		if u.checkSleep() {
			return
		}
		if u.sleeping {
			return
		}
	}

	switch u.page {
	case PageMain:
		u.updateMain()
	case PageWiFiCfg:
		u.updateWiFiConfig()
	}

	u.firstDraw = false
}

// ---------------------------------------------------------------------------
// Main page
// ---------------------------------------------------------------------------

// updateMain renders the main BMC status page on the OLED.
//
// Sub-page 0 (default):
//
//	Row 0 (page 0): ETH icon + WiFi icon
//	Row 2 (page 2): IP address prefixed with "E:" or "W:"
//	Row 4 (page 4): Host power state ("PWR:ON" / "PWR:OFF")
//	Row 6 (page 6): "NanoKVM" branding
//
// Sub-page 1: display is logically "sleeping" (cleared). The C++ source
// sets sub_page=1 when the auto-sleep timer expires; we handle that via
// checkSleep() instead, keeping the display off.
func (u *UI) updateMain() {
	if u.subPage == 1 {
		// Sleep sub-page: clear and done.
		if u.firstDraw {
			u.oled.Clear()
		}
		return
	}

	// Advance IP swap timer.
	u.ipSwapTimer++
	if u.ipSwapTimer >= ipSwapInterval {
		u.ipSwapTimer = 0
		u.showEthIP = !u.showEthIP
	}

	ns := u.netMon.State()

	// Determine which IP to actually show. Mirroring the C++ show_which_ip:
	// - If WiFi doesn't exist, always show ETH.
	// - If only one network is connected, show that one.
	// - Otherwise alternate based on showEthIP flag.
	ethConnected := ns.Eth.State >= netmon.NICStateRunning && ns.Eth.IPAddr != ""
	wifiConnected := ns.WiFi.State >= netmon.NICStateRunning && ns.WiFi.IPAddr != ""

	showEth := u.showEthIP
	if ns.WiFi.State == netmon.NICStateNoExist {
		showEth = true
	} else if ethConnected && !wifiConnected {
		showEth = true
	} else if !ethConnected && wifiConnected {
		showEth = false
	}

	ipPrefix := "E:"
	ipAddr := ns.Eth.IPAddr
	if !showEth {
		ipPrefix = "W:"
		ipAddr = ns.WiFi.IPAddr
	}
	if ipAddr == "" {
		ipAddr = "--"
	}

	ipChanged := (ipAddr != u.prevEthIP && showEth) || (ipAddr != u.prevWiFiIP && !showEth)
	if showEth {
		u.prevEthIP = ipAddr
	} else {
		u.prevWiFiIP = ipAddr
	}

	needRedraw := u.firstDraw || ipChanged

	if u.firstDraw {
		u.oled.Clear()
	}

	if needRedraw {
		u.drawMainIcons(&ns)
		u.drawIPLine(ipPrefix, ipAddr)
		u.drawPowerState()
		u.drawBranding()
	}
}

// drawMainIcons renders the ETH and WiFi state icons on row 0.
// For the PCIe hardware the C++ uses x0=24, x1=43; for Cube x0=84, x1=104.
// Since the BMC simplifies to just these two icons in the top row, we use
// positions matching the PCIe layout (the more constrained display).
func (u *UI) drawMainIcons(ns *netmon.NetworkState) {
	var ethIcon uint8 = iconETHOff
	if ns.Eth.State >= netmon.NICStateUp {
		ethIcon = iconETHOn
	}

	var wifiIcon uint8 = iconWiFiOff
	switch {
	case ns.WiFi.State == netmon.NICStateNoExist:
		// No WiFi adapter: show a blank placeholder.
		u.oled.ShowString(40, 0, " -- ", 4)
		u.oled.ShowState(0, 0, ethIcon, iconSize)
		return
	case ns.WiFi.State >= netmon.NICStateUp:
		wifiIcon = iconWiFiOn
	}

	u.oled.ShowState(0, 0, ethIcon, iconSize)
	u.oled.ShowState(40, 0, wifiIcon, iconSize)
}

// drawIPLine shows the IP address on OLED page 2 (pixel row 16).
// Format: "E:192.168.1.100" or "W:10.10.10.1"
func (u *UI) drawIPLine(prefix, ip string) {
	// Clear the row first.
	u.oled.ShowString(0, 2, "                     ", 4)
	text := prefix + ip
	u.oled.ShowString(0, 2, text, 4)
}

// drawPowerState reads the host power GPIO and displays the power state
// on OLED page 4 (pixel row 32).
func (u *UI) drawPowerState() {
	pwr := sysctl.ReadHostPowerState(u.powerGPIOPath)
	label := "PWR:OFF"
	if pwr == 1 {
		label = "PWR:ON"
	}
	u.oled.ShowString(0, 4, "            ", 8)
	u.oled.ShowString(0, 4, label, 8)
}

// drawBranding renders "NanoKVM" on OLED page 6 (pixel row 48).
func (u *UI) drawBranding() {
	u.oled.ShowString(0, 6, "NanoKVM", 8)
}

// ---------------------------------------------------------------------------
// WiFi configuration page
// ---------------------------------------------------------------------------

// updateWiFiConfig renders the WiFi AP config flow pages.
// Sub-pages map to the C++ kvm_wifi_config_ui_disp switch:
//
//	0 – "WiFi AP is Starting.."
//	1 – QR code for WIFI:T:WPA2;S:NanoKVM;P:<pass>;;
//	2 – Text: SSID + password
//	3 – QR code for http://<ip>/#/WIFI?P=<pass>
//	4 – Text: Config URL + IP
//	5 – "WiFi Connect..."
//
// Sub-pages 1/2 and 3/4 auto-alternate every qrSwapInterval ticks.
func (u *UI) updateWiFiConfig() {
	// Auto-swap between QR and text representations.
	u.qrSwapTimer++
	if u.qrSwapTimer >= qrSwapInterval {
		u.qrSwapTimer = 0
		u.showQR = !u.showQR
	}

	wifiState := u.wifiCfg.State
	pass := u.wifiCfg.APPassword

	// Map WiFi config state to sub-page if the config state machine drives it.
	switch wifiState {
	case wificonfig.StateStarting:
		u.subPage = 0
	case wificonfig.StateWaitConnect:
		if u.showQR {
			u.subPage = 1
		} else {
			u.subPage = 2
		}
	case wificonfig.StateWaitCreds:
		if u.showQR {
			u.subPage = 3
		} else {
			u.subPage = 4
		}
	case wificonfig.StateTryConnect:
		u.subPage = 5
	}

	if !u.firstDraw {
		return
	}

	switch u.subPage {
	case 0:
		u.showWiFiStarting()
	case 1:
		u.showWiFiAPQR(pass)
	case 2:
		u.showWiFiAPText(pass)
	case 3:
		u.showWiFiConfigQR(pass)
	case 4:
		u.showWiFiConfigIP(pass)
	case 5:
		u.showWiFiConnecting()
	}

	u.firstDraw = false
}

// showWiFiStarting displays "WiFi AP is Starting..".
func (u *UI) showWiFiStarting() {
	u.oled.Clear()
	u.oled.ShowString(0, 1, "WiFi AP is", 8)
	u.oled.ShowString(0, 2, "Starting..", 8)
}

// showWiFiAPQR renders a QR code encoding the WiFi AP credentials.
func (u *UI) showWiFiAPQR(pass string) {
	data := fmt.Sprintf("WIFI:T:WPA2;S:NanoKVM;P:%s;;", pass)
	u.oled.Clear()
	u.showQRCode(data)
	u.oled.ShowStringTurn(3, 1, "WiFi", 8)
	u.oled.ShowStringTurn(3, 2, "AP:", 8)
}

// showWiFiAPText renders the SSID and password as plain text.
func (u *UI) showWiFiAPText(pass string) {
	u.oled.Clear()
	u.oled.ShowString(0, 0, "SSID:", 8)
	u.oled.ShowStringAlignRight(63, 1, "NanoKVM", 8)
	u.oled.ShowString(0, 2, "PASS:", 8)
	u.oled.ShowStringAlignRight(63, 3, pass, 8)
}

// showWiFiConfigQR renders a QR code encoding the configuration URL.
func (u *UI) showWiFiConfigQR(pass string) {
	ns := u.netMon.State()
	ip := ns.WiFi.IPAddr
	if ip == "" {
		ip = "10.10.10.1"
	}
	url := fmt.Sprintf("http://%s/#/WIFI?P=%s", ip, pass)
	u.oled.Clear()
	u.showQRCode(url)
	u.oled.ShowStringTurn(3, 1, "Web:", 8)
}

// showWiFiConfigIP renders the configuration URL as plain text.
func (u *UI) showWiFiConfigIP(pass string) {
	ns := u.netMon.State()
	ip := ns.WiFi.IPAddr
	if ip == "" {
		ip = "10.10.10.1"
	}
	u.oled.Clear()
	u.oled.ShowString(1, 0, "Config URL", 8)
	u.oled.ShowStringAlignRight(63, 1, "----------------", 4)
	u.oled.ShowStringAlignRight(63, 2, ip+"/#/", 4)
	u.oled.ShowStringAlignRight(63, 3, fmt.Sprintf("WIFI?P=%s", pass), 4)
}

// showWiFiConnecting displays "WiFi Connect...".
func (u *UI) showWiFiConnecting() {
	u.oled.Clear()
	u.oled.ShowString(0, 1, "WiFi", 8)
	u.oled.ShowString(0, 2, "Connect...", 8)
}

// ---------------------------------------------------------------------------
// QR code rendering
// ---------------------------------------------------------------------------

// showQRCode generates a QR code bitmap and renders it on the OLED.
//
// The C++ implementation uses a 29×29 module QR (version 3), starting at
// pixel offset (2,2) within a 33×4 (33 columns × 4 pages = 32 pixel rows)
// buffer. We replicate that layout: fill buffer with 0xFF (white), punch
// black modules, then blit at x=29, y=0 via ShowIMG.
func (u *UI) showQRCode(text string) {
	qr, err := qrcode.New(text, qrcode.Low)
	if err != nil {
		return
	}
	qr.DisableBorder = true
	bitmap := qr.Bitmap() // [][]bool, true = black

	const (
		beginX  = 2
		beginY  = 2
		imgW    = 33 // columns
		imgH    = 4  // pages (each page = 8 pixel rows)
		imgSize = imgW * imgH
	)

	buf := make([]byte, imgSize)
	for i := range buf {
		buf[i] = 0xFF
	}

	// Map the QR bitmap into the column-major, page-oriented OLED format.
	// i = row (vertical), j = column (horizontal).
	rows := len(bitmap)
	if rows > 29 {
		rows = 29
	}
	for i := 0; i < rows; i++ {
		cols := len(bitmap[i])
		if cols > 29 {
			cols = 29
		}
		for j := 0; j < cols; j++ {
			if bitmap[i][j] {
				py := i + beginY
				px := j + beginX
				idx := (py/8)*imgW + px
				if idx < imgSize {
					mask := ^(byte(1) << uint(py%8))
					buf[idx] &= mask
				}
			}
		}
	}

	u.oled.Fill()
	u.oled.ShowIMG(29, 0, buf, imgW, imgH)
}
