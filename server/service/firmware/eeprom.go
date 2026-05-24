package firmware

// eeprom.go is the BMC-side facade over the RPi 5 bootloader EEPROM. The
// device-runtime EEPROM flash is owned by rpi-eeprom-update on the host —
// we never poke the EEPROM directly. The BMC only manages the FAT volume
// the host sees over the USB gadget.
//
// Reads:
//   The currently-installed config is displayed from eeprom.txt, which
//   U-Boot writes to the FAT root on every boot. That's the authoritative
//   live state. We do NOT extract bootconf.txt from pieeprom.bin for
//   display — the bin file may be a freshly-downloaded blank template
//   that doesn't match what's actually programmed.
//
// Writes:
//   Saves go through SetEEPROMConfig which:
//     1. Ensures a pieeprom.bin is on the FAT (downloads the latest from
//        upstream rpi-eeprom if missing — see eepromupdater).
//     2. Loads the source image (the pending pieeprom.upd if a previous
//        edit hasn't been flashed yet, else pieeprom.bin).
//     3. Uses the rpieeprom parser to swap the embedded bootconf.txt
//        section for the new content.
//     4. Writes the modified bytes back as pieeprom.upd.
//   On next boot rpi-eeprom-update sees pieeprom.upd and flashes the
//   EEPROM safely.

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/BMCPi/NanoKVM/server/service/firmware/eepromkeys"
	"github.com/BMCPi/NanoKVM/server/service/firmware/eepromupdater"
	"github.com/BMCPi/NanoKVM/server/service/firmware/rpieeprom"

	log "github.com/sirupsen/logrus"
)

// PlatformDefault is the EEPROM-key catalog platform this BMC manages.
// Hardcoded to Pi 5 because the device target is Pi 5; extend later if
// we add Pi 4 support (e.g. read from config + the firmware inventory).
const PlatformDefault = eepromkeys.PlatformRPi5

const (
	// File names on the firmware-image FAT root.
	eepromTextFile    = "eeprom.txt"   // U-Boot's read-only text dump
	eepromBinaryFile  = "pieeprom.bin" // current EEPROM image (cached)
	eepromPendingFile = "pieeprom.upd" // staged update for rpi-eeprom-update
)

// maxEEPROMConfigBytes caps accepted writes. bootconf.txt sits in the
// modifiable-file partition whose per-file ceiling is MaxFileSize
// (~4 KB - header). This is the stricter guard we apply BEFORE handing
// the bytes to the parser; the parser enforces its own limit on top.
const maxEEPROMConfigBytes = rpieeprom.MaxFileSize

// EEPROMSetting is one parsed key=value line for UI display. Section is
// the most-recent [section] header above the line (defaults to "all").
type EEPROMSetting struct {
	Section string `json:"section"`
	Key     string `json:"key"`
	Value   string `json:"value"`
}

// EEPROMConfigSummary is the API-facing structure: raw text + section-
// grouped view + ordered section names + pending-update flag.
type EEPROMConfigSummary struct {
	Raw      string                     `json:"raw"`
	Sections map[string][]EEPROMSetting `json:"sections"`
	Order    []string                   `json:"order"`
	// Pending is true when a staged pieeprom.upd is present on the FAT
	// waiting for the next boot to be flashed by rpi-eeprom-update.
	Pending bool `json:"pending"`
	// Source tells the caller where the displayed text came from
	// (eeprom.txt currently; reserved for future variants).
	Source string `json:"source"`
	// PieepromBinPresent reports whether the FAT already holds a
	// pieeprom.bin. Edits download one automatically if missing.
	PieepromBinPresent bool `json:"pieepromBinPresent"`
	// Catalog is the documented EEPROM-key reference for the active
	// platform (name/type/default/description). Lets clients build a
	// structured editor that shows the default beside each value.
	Catalog []eepromkeys.Key `json:"catalog"`
	// Platform is the catalog scope (e.g. "2712" for RPi 5).
	Platform eepromkeys.Platform `json:"platform"`
}

// GetEEPROMConfig returns the current bootloader config from eeprom.txt
// (U-Boot's per-boot text dump) plus flags describing the state of the
// staged-update files on the FAT.
func (c *Controller) GetEEPROMConfig() (EEPROMConfigSummary, error) {
	pending, _ := c.hasFileOnImage(eepromPendingFile)
	binPresent, _ := c.hasFileOnImage(eepromBinaryFile)

	var text string
	var source string
	if data, err := c.ReadFileFromImage(eepromTextFile); err == nil && len(data) > 0 {
		text = string(data)
		source = eepromTextFile
	}

	summary := summarise(text, source, pending)
	summary.PieepromBinPresent = binPresent
	summary.Platform = PlatformDefault
	summary.Catalog = eepromkeys.ForPlatform(PlatformDefault)
	return summary, nil
}

// SetEEPROMConfig stages a bootloader config change as pieeprom.upd. If
// pieeprom.bin isn't on the FAT yet, the latest upstream image is fetched
// from raspberrypi/rpi-eeprom first.
//
// Returns the new summary so the UI doesn't need a follow-up GET. The
// returned Pending is always true on success.
func (c *Controller) SetEEPROMConfig(ctx context.Context, bootconfTxt string) (EEPROMConfigSummary, error) {
	if err := validateBootconfBytes([]byte(bootconfTxt)); err != nil {
		return EEPROMConfigSummary{}, err
	}
	normalised := normaliseLineEndings(bootconfTxt)
	// Drop key=value lines in [all] whose value matches the documented
	// platform default. Keeps bootconf.txt minimal so the operator sees
	// only the intentional deviations, and matches rpi-eeprom-config's
	// own convention (bootloader applies its own defaults for anything
	// not explicitly set).
	normalised = eepromkeys.FilterDefaultsFromBootconf(PlatformDefault, normalised)

	if err := c.EnsurePieepromBin(ctx); err != nil {
		return EEPROMConfigSummary{}, fmt.Errorf("ensure pieeprom.bin: %w", err)
	}

	source, sourceName, err := c.loadEEPROMSourceImage()
	if err != nil {
		return EEPROMConfigSummary{}, err
	}

	img, err := rpieeprom.ParseBytes(source)
	if err != nil {
		return EEPROMConfigSummary{}, fmt.Errorf("parse %s: %w", sourceName, err)
	}
	if err := img.UpdateFile(rpieeprom.BootConfTxt, []byte(normalised)); err != nil {
		return EEPROMConfigSummary{}, fmt.Errorf("replace bootconf.txt: %w", err)
	}

	if err := c.WriteFileToImage(eepromPendingFile, img.Bytes()); err != nil {
		return EEPROMConfigSummary{}, fmt.Errorf("write %s: %w", eepromPendingFile, err)
	}

	// Refresh the summary: text still reads from eeprom.txt (current
	// live state) — what we just wrote is staged, not active.
	out, _ := c.GetEEPROMConfig()
	out.Pending = true
	return out, nil
}

// GetBIOSAttributes returns the live [all]-section keys from eeprom.txt
// as a flat map suitable for the Redfish Bios.Attributes resource.
// Conditional sections ([gpio4=1] etc.) are not exposed via this surface.
func (c *Controller) GetBIOSAttributes() (map[string]string, error) {
	data, err := c.ReadFileFromImage(eepromTextFile)
	if err != nil || len(data) == 0 {
		// Empty map (not nil) so downstream callers can json-encode without
		// special-casing absence.
		return map[string]string{}, nil
	}
	return eepromkeys.ParseAllSection(string(data)), nil
}

// GetPendingBIOSAttributes returns the [all]-section keys staged in
// pieeprom.upd if a pending update is present, else nil. Used by the
// Redfish Bios.Settings (SettingsObject) resource.
func (c *Controller) GetPendingBIOSAttributes() (map[string]string, bool, error) {
	data, err := c.ReadFileFromImage(eepromPendingFile)
	if err != nil || len(data) == 0 {
		return nil, false, nil
	}
	img, err := rpieeprom.ParseBytes(data)
	if err != nil {
		return nil, false, fmt.Errorf("parse pending image: %w", err)
	}
	bc, err := img.GetFile(rpieeprom.BootConfTxt)
	if err != nil {
		return nil, false, fmt.Errorf("extract pending bootconf.txt: %w", err)
	}
	return eepromkeys.ParseAllSection(string(bc)), true, nil
}

// SetBIOSAttributes stages a Redfish PATCH against the BIOS settings
// resource. The incoming attrs replace the entire [all] section; any
// conditional sections ([gpio4=1] etc.) currently in the source bootconf
// are preserved verbatim. Default values are stripped before writing.
//
// "Source" bootconf is the same chain as SetEEPROMConfig: prefer the
// already-pending .upd (so two PATCHes chain), else pieeprom.bin (which
// EnsurePieepromBin guarantees exists).
func (c *Controller) SetBIOSAttributes(ctx context.Context, attrs map[string]string) (EEPROMConfigSummary, error) {
	// Read current source bootconf so we can keep its non-[all] sections.
	if err := c.EnsurePieepromBin(ctx); err != nil {
		return EEPROMConfigSummary{}, fmt.Errorf("ensure pieeprom.bin: %w", err)
	}
	source, sourceName, err := c.loadEEPROMSourceImage()
	if err != nil {
		return EEPROMConfigSummary{}, err
	}
	img, err := rpieeprom.ParseBytes(source)
	if err != nil {
		return EEPROMConfigSummary{}, fmt.Errorf("parse %s: %w", sourceName, err)
	}
	existingBootconf := ""
	if bc, err := img.GetFile(rpieeprom.BootConfTxt); err == nil {
		existingBootconf = string(bc)
	}

	nonAll := eepromkeys.ExtractNonAllSections(existingBootconf)
	newBootconf := eepromkeys.SerializeAllSection(PlatformDefault, attrs, nonAll)

	if err := validateBootconfBytes([]byte(newBootconf)); err != nil {
		return EEPROMConfigSummary{}, err
	}
	if err := img.UpdateFile(rpieeprom.BootConfTxt, []byte(newBootconf)); err != nil {
		return EEPROMConfigSummary{}, fmt.Errorf("replace bootconf.txt: %w", err)
	}
	if err := c.WriteFileToImage(eepromPendingFile, img.Bytes()); err != nil {
		return EEPROMConfigSummary{}, fmt.Errorf("write %s: %w", eepromPendingFile, err)
	}
	out, _ := c.GetEEPROMConfig()
	out.Pending = true
	return out, nil
}

// CancelEEPROMUpdate removes any staged pieeprom.upd. Next read will show
// the live config (from eeprom.txt) with Pending=false.
func (c *Controller) CancelEEPROMUpdate() error {
	if err := c.RemoveFileFromImage(eepromPendingFile); err != nil {
		return fmt.Errorf("remove %s: %w", eepromPendingFile, err)
	}
	return nil
}

// EnsurePieepromBin makes sure pieeprom.bin exists on the FAT. If it does
// not, the latest upstream stable RPi 5 image is downloaded and written.
// No-op when the file is already present.
func (c *Controller) EnsurePieepromBin(ctx context.Context) error {
	if ok, _ := c.hasFileOnImage(eepromBinaryFile); ok {
		return nil
	}
	return c.refreshPieepromBinLocked(ctx, false)
}

// RefreshPieepromBin force-downloads the latest upstream stable image and
// overwrites pieeprom.bin on the FAT. Use when the user wants to pick up
// a new bootloader release. Does NOT touch pieeprom.upd: a pending update
// continues to ride on the OLD bin until it's flashed or discarded.
func (c *Controller) RefreshPieepromBin(ctx context.Context) error {
	return c.refreshPieepromBinLocked(ctx, true)
}

// LatestPieepromImage queries upstream for the latest stable RPi 5
// EEPROM image metadata. Exposed for UIs that want to display "new
// version available" without committing to a download.
func (c *Controller) LatestPieepromImage(ctx context.Context) (*eepromupdater.Image, error) {
	return eepromupdater.FindLatest(ctx, eepromupdater.FindLatestOptions{
		Platform: eepromupdater.PlatformRPi5,
		Channel:  eepromupdater.ChannelStable,
	})
}

func (c *Controller) refreshPieepromBinLocked(ctx context.Context, force bool) error {
	if ctx == nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
	}

	log.Info("eeprom: fetching latest pieeprom.bin from upstream")
	img, err := eepromupdater.FindLatest(ctx, eepromupdater.FindLatestOptions{
		Platform: eepromupdater.PlatformRPi5,
		Channel:  eepromupdater.ChannelStable,
	})
	if err != nil {
		return fmt.Errorf("find latest: %w", err)
	}
	log.Infof("eeprom: downloading %s (%d bytes)", img.Name, img.Size)

	data, err := eepromupdater.Download(ctx, img, nil)
	if err != nil {
		return fmt.Errorf("download %s: %w", img.Name, err)
	}

	// Validate the downloaded blob is a recognisable EEPROM image before
	// staging it — better to fail here than to write a junk file the
	// host then tries to flash.
	if _, err := rpieeprom.ParseBytes(data); err != nil {
		return fmt.Errorf("downloaded %s failed structural validation: %w", img.Name, err)
	}

	if err := c.WriteFileToImage(eepromBinaryFile, data); err != nil {
		return fmt.Errorf("write %s: %w", eepromBinaryFile, err)
	}
	log.Infof("eeprom: wrote %s as %s on firmware FAT (force=%v)", img.Name, eepromBinaryFile, force)
	return nil
}

// loadEEPROMSourceImage reads the EEPROM image we should mutate when
// staging a new pieeprom.upd: the existing pending .upd first (so two
// edits in a row chain), else pieeprom.bin (which EnsurePieepromBin
// guarantees exists by this point).
func (c *Controller) loadEEPROMSourceImage() ([]byte, string, error) {
	if data, err := c.ReadFileFromImage(eepromPendingFile); err == nil && len(data) > 0 {
		return data, eepromPendingFile, nil
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, "", fmt.Errorf("read %s: %w", eepromPendingFile, err)
	}
	if data, err := c.ReadFileFromImage(eepromBinaryFile); err == nil && len(data) > 0 {
		return data, eepromBinaryFile, nil
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, "", fmt.Errorf("read %s: %w", eepromBinaryFile, err)
	}
	return nil, "", fmt.Errorf("no EEPROM image available on the firmware FAT after ensure-bin (looked for %s, %s)",
		eepromPendingFile, eepromBinaryFile)
}

// hasFileOnImage reports whether a file at the FAT root exists and is
// non-empty. Wraps ReadFileFromImage which already returns (nil, nil) for
// not-found.
func (c *Controller) hasFileOnImage(name string) (bool, error) {
	data, err := c.ReadFileFromImage(name)
	if err != nil {
		return false, err
	}
	return len(data) > 0, nil
}

// validateBootconfBytes is the BMC-side guard before handing bytes to the
// parser: size cap + printable-text only. The parser enforces its own
// MaxFileSize on top.
func validateBootconfBytes(b []byte) error {
	if len(b) > maxEEPROMConfigBytes {
		return fmt.Errorf("bootconf.txt too large (%d bytes, max %d)", len(b), maxEEPROMConfigBytes)
	}
	for i, r := range b {
		if r == '\t' || r == '\n' || r == '\r' || (r >= 0x20 && r < 0x7f) {
			continue
		}
		return fmt.Errorf("bootconf.txt contains non-printable byte at offset %d (0x%02x)", i, r)
	}
	return nil
}

func normaliseLineEndings(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n")
}

// ParseEEPROMConfig parses bootconf.txt-shaped INI text into a flat list
// of settings for UI display. Order follows source-file order. Comments
// and blank lines are skipped; section headers ([all], [gpio4=1], …)
// become the Section field on subsequent settings.
func ParseEEPROMConfig(content string) []EEPROMSetting {
	out := make([]EEPROMSetting, 0, 16)
	section := "all"
	scanner := bufio.NewScanner(bytes.NewReader([]byte(content)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.TrimSpace(line[1 : len(line)-1])
			if section == "" {
				section = "all"
			}
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq <= 0 {
			continue
		}
		key := strings.TrimSpace(line[:eq])
		value := strings.TrimSpace(line[eq+1:])
		if key == "" {
			continue
		}
		out = append(out, EEPROMSetting{Section: section, Key: key, Value: value})
	}
	return out
}

// summarise builds the API-facing structure from raw text + provenance.
func summarise(content, source string, pending bool) EEPROMConfigSummary {
	parsed := ParseEEPROMConfig(content)
	sections := make(map[string][]EEPROMSetting)
	seen := make(map[string]bool)
	order := make([]string, 0, 4)
	for _, s := range parsed {
		if !seen[s.Section] {
			seen[s.Section] = true
			order = append(order, s.Section)
		}
		sections[s.Section] = append(sections[s.Section], s)
	}
	if len(order) == 0 {
		for s := range sections {
			order = append(order, s)
		}
		sort.Strings(order)
	}
	return EEPROMConfigSummary{
		Raw:      content,
		Sections: sections,
		Order:    order,
		Pending:  pending,
		Source:   source,
	}
}

// EEPROMConfigSummaryOf preserves a backward-compatible helper: build a
// summary from a known text blob (no provenance / no pending detection).
func EEPROMConfigSummaryOf(content string) EEPROMConfigSummary {
	return summarise(content, "", false)
}
