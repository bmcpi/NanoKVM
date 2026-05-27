package firmware

// eeprom.go is the BMC-side facade over the RPi 5 bootloader EEPROM. The
// device-runtime EEPROM flash is owned by rpi-eeprom-update on the host —
// we never poke the EEPROM directly. The BMC only manages the FAT volume
// the host sees over the USB gadget.
//
// Reads:
//   U-Boot now exports the raw pieeprom.bin (a dump of the currently-
//   programmed EEPROM) on every boot. The BMC parses it through the
//   rpieeprom package to extract the embedded bootconf.txt and the
//   build timestamp baked into bootcode.bin. The previous eeprom.txt
//   text dump is gone.
//
// Writes:
//   Saves go through SetEEPROMConfig which:
//     1. Loads the source image — the pending pieeprom.upd if a previous
//        edit hasn't been flashed yet, else the live pieeprom.bin.
//     2. Uses the rpieeprom parser to swap the embedded bootconf.txt
//        section for the new content.
//     3. Writes the modified bytes back as pieeprom.upd.
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
	eepromBinaryFile   = "pieeprom.bin" // raw EEPROM dump written by U-Boot
	eepromPendingFile  = "pieeprom.upd" // staged update for rpi-eeprom-update
	eepromRecoveryFile = "recovery.bin" // recovery loader sourced from upstream
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
	// (e.g. "pieeprom.bin" for the live image, "pieeprom.upd" when a
	// staged update is being previewed).
	Source string `json:"source"`
	// PieepromBinPresent reports whether the FAT holds a pieeprom.bin.
	// Under the current U-Boot it should always be true; the flag is
	// kept so the UI can surface a clear "no image yet — reboot" hint
	// while the BMC starts up before U-Boot has written one.
	PieepromBinPresent bool `json:"pieepromBinPresent"`
	// RecoveryBinPresent reports whether recovery.bin is staged on the
	// FAT. rpi-eeprom-update needs it to actually apply a pending
	// pieeprom.upd at next boot; missing it means the staged update is
	// inert until the file is downloaded.
	RecoveryBinPresent bool `json:"recoveryBinPresent"`
	// Version is the bootloader build timestamp parsed from the image
	// (BUILD_TIMESTAMP marker inside bootcode). Empty when the image
	// is missing or doesn't carry the marker.
	Version string `json:"version,omitempty"`
	// VersionUnix is the same value as a Unix timestamp; 0 when unknown.
	VersionUnix int64 `json:"versionUnix,omitempty"`
	// Catalog is the documented EEPROM-key reference for the active
	// platform (name/type/default/description). Lets clients build a
	// structured editor that shows the default beside each value.
	Catalog []eepromkeys.Key `json:"catalog"`
	// Platform is the catalog scope (e.g. "2712" for RPi 5).
	Platform eepromkeys.Platform `json:"platform"`
}

// GetEEPROMConfig returns the current bootloader config parsed out of
// pieeprom.bin — the raw EEPROM dump U-Boot writes to the FAT root on
// every boot — plus the bootloader build version and flags describing
// the staged-update slot.
//
// If a pieeprom.upd is staged its bootconf.txt is shown instead, so the
// UI reflects what the host will see after the next flash. Source on
// the summary tells the caller which file was read.
func (c *Controller) GetEEPROMConfig() (EEPROMConfigSummary, error) {
	pending, _ := c.hasFileOnImage(eepromPendingFile)
	binPresent, _ := c.hasFileOnImage(eepromBinaryFile)
	recoveryPresent, _ := c.hasFileOnImage(eepromRecoveryFile)

	text, source, version, versionUnix := c.readBootconfFromImage()

	summary := summarise(text, source, pending)
	summary.PieepromBinPresent = binPresent
	summary.RecoveryBinPresent = recoveryPresent
	summary.Platform = PlatformDefault
	summary.Catalog = eepromkeys.ForPlatform(PlatformDefault)
	summary.Version = version
	summary.VersionUnix = versionUnix
	return summary, nil
}

// readBootconfFromImage opens the most-relevant EEPROM image on the FAT
// (pending .upd if any, else the live .bin) and extracts the embedded
// bootconf.txt plus the build timestamp. Returns empty strings when no
// image is present or the parse fails — callers treat that as "live
// config not yet available" rather than as an error, because the
// situation is recoverable (U-Boot will write a fresh .bin on next
// boot).
func (c *Controller) readBootconfFromImage() (text, source, version string, versionUnix int64) {
	candidates := []string{eepromPendingFile, eepromBinaryFile}
	for _, name := range candidates {
		data, err := c.ReadFileFromImage(name)
		if err != nil || len(data) == 0 {
			continue
		}
		img, err := rpieeprom.ParseBytes(data)
		if err != nil {
			log.Debugf("eeprom: parse %s failed: %v", name, err)
			continue
		}
		source = name
		if bc, err := img.GetFile(rpieeprom.BootConfTxt); err == nil {
			text = string(bc)
		} else {
			log.Debugf("eeprom: extract bootconf.txt from %s failed: %v", name, err)
		}
		if v, ok := img.Version(); ok {
			version = v.Format("2006-01-02")
			versionUnix = v.Unix()
		}
		return text, source, version, versionUnix
	}
	return "", "", "", 0
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

	// rpi-eeprom-update needs recovery.bin on the same FAT to actually
	// apply pieeprom.upd at next boot. Fetch it from upstream lazily —
	// if the file is already present we leave it alone, so a previous
	// successful stage is enough.
	if err := c.EnsureRecoveryBin(ctx); err != nil {
		log.Warnf("eeprom: ensure recovery.bin failed: %v (staged update may not flash)", err)
	}

	// Refresh the summary: GetEEPROMConfig prefers the pending .upd so
	// the returned text matches what was just staged. Pending=true is
	// asserted below in case the read raced anything else.
	out, _ := c.GetEEPROMConfig()
	out.Pending = true
	return out, nil
}

// eepromImageCandidates lists the FAT-root EEPROM images we look at in
// priority order for live-state reads. The pending .upd wins when
// present so the Redfish/UI view matches what the host will see on the
// next boot; otherwise the live .bin U-Boot writes each boot is used.
var eepromImageCandidates = []string{eepromPendingFile, eepromBinaryFile}

// EEPROMReadDiagnostics describes everything the BMC tried when fetching
// live bootloader settings. Surfaced via the Redfish Bios resource (Oem
// extension) so an operator can see WHY Attributes might be empty.
type EEPROMReadDiagnostics struct {
	// Probes is the result of probing each candidate path, in order.
	Probes []EEPROMProbe `json:"probes"`
	// Source is the path we ultimately read from. Empty when nothing
	// was found.
	Source string `json:"source"`
	// AttributeCount is how many key=value pairs the [all]-section
	// parse produced from Source.
	AttributeCount int `json:"attributeCount"`
	// SectionsSeen lists every section header found in the source file
	// (lowercase). Useful when [all] is empty but conditional sections
	// hold all the actual settings.
	SectionsSeen []string `json:"sectionsSeen,omitempty"`
	// Version is the bootloader build timestamp parsed from the image,
	// formatted YYYY-MM-DD. Empty when the marker is missing.
	Version string `json:"version,omitempty"`
}

// EEPROMProbe is one path-lookup attempt during EEPROM read.
type EEPROMProbe struct {
	Path       string `json:"path"`
	Found      bool   `json:"found"`
	Size       int    `json:"size,omitempty"`
	Error      string `json:"error,omitempty"`
	ParseError string `json:"parseError,omitempty"`
}

// GetBIOSAttributes returns the live [all]-section keys from the
// bootloader image's embedded bootconf.txt as a flat map suitable for
// the Redfish Bios.Attributes resource, plus diagnostics describing
// what was probed. Conditional sections ([gpio4=1] etc.) are not
// exposed via this surface.
//
// FS errors and parse failures are recorded on the diagnostics rather
// than returned, so a single bad file doesn't poison the whole read —
// the next candidate (e.g. the live .bin behind a malformed .upd) is
// still tried. The function only returns an error for unexpected
// failures it can't recover from.
func (c *Controller) GetBIOSAttributes() (map[string]string, EEPROMReadDiagnostics, error) {
	diag := EEPROMReadDiagnostics{Probes: make([]EEPROMProbe, 0, len(eepromImageCandidates))}

	for _, name := range eepromImageCandidates {
		data, err := c.ReadFileFromImage(name)
		probe := EEPROMProbe{Path: name}
		if err != nil {
			probe.Error = err.Error()
			diag.Probes = append(diag.Probes, probe)
			continue
		}
		if len(data) == 0 {
			diag.Probes = append(diag.Probes, probe)
			continue
		}
		probe.Found = true
		probe.Size = len(data)

		img, err := rpieeprom.ParseBytes(data)
		if err != nil {
			probe.ParseError = err.Error()
			diag.Probes = append(diag.Probes, probe)
			continue
		}
		bc, err := img.GetFile(rpieeprom.BootConfTxt)
		if err != nil {
			probe.ParseError = "extract bootconf.txt: " + err.Error()
			diag.Probes = append(diag.Probes, probe)
			continue
		}
		diag.Probes = append(diag.Probes, probe)

		// First image with a parseable bootconf wins. Capture diagnostics
		// so the operator can spot "all settings live in a conditional
		// section" cases.
		text := string(bc)
		diag.Source = name
		diag.SectionsSeen = eepromkeys.ListSections(text)
		if v, ok := img.Version(); ok {
			diag.Version = v.Format("2006-01-02")
		}
		attrs := eepromkeys.ParseAllSection(text)
		diag.AttributeCount = len(attrs)
		return attrs, diag, nil
	}

	// Nothing found — return empty map (not nil) so json-encoding stays
	// `"Attributes": {}` rather than `"Attributes": null`.
	return map[string]string{}, diag, nil
}

// EEPROMPendingDiagnostics describes the staged-update slot's state so
// /redfish/v1/Systems/1/Bios/Settings can answer "why is this empty?".
// Surfaced via the Redfish Oem extension on that resource.
type EEPROMPendingDiagnostics struct {
	// Path is the FAT-root filename we look at for staged updates.
	Path string `json:"path"`
	// Present is true when pieeprom.upd exists and is non-empty.
	Present bool `json:"present"`
	// Size is the staged image's size in bytes. Zero when not present.
	Size int `json:"size,omitempty"`
	// ParseError describes a failure to walk the staged image (e.g. truncated
	// download). Empty on success or when no .upd exists.
	ParseError string `json:"parseError,omitempty"`
	// AttributeCount is how many [all]-section keys the staged bootconf.txt
	// contains. Zero when no update is staged OR when the staged config has
	// only conditional sections.
	AttributeCount int `json:"attributeCount"`
}

// GetPendingBIOSAttributes returns the [all]-section keys staged in
// pieeprom.upd if a pending update is present, else nil. The diagnostics
// struct describes the .upd slot's state so callers (Redfish Settings
// resource) can distinguish "no update staged" from "FS error" from
// "staged file is malformed".
func (c *Controller) GetPendingBIOSAttributes() (map[string]string, bool, EEPROMPendingDiagnostics, error) {
	diag := EEPROMPendingDiagnostics{Path: eepromPendingFile}

	data, err := c.ReadFileFromImage(eepromPendingFile)
	if err != nil {
		return nil, false, diag, err
	}
	if len(data) == 0 {
		// File not present (the common case — nothing has been staged).
		return nil, false, diag, nil
	}
	diag.Present = true
	diag.Size = len(data)

	img, err := rpieeprom.ParseBytes(data)
	if err != nil {
		diag.ParseError = err.Error()
		return nil, false, diag, fmt.Errorf("parse pending image: %w", err)
	}
	bc, err := img.GetFile(rpieeprom.BootConfTxt)
	if err != nil {
		diag.ParseError = "extract bootconf.txt: " + err.Error()
		return nil, false, diag, fmt.Errorf("extract pending bootconf.txt: %w", err)
	}
	attrs := eepromkeys.ParseAllSection(string(bc))
	diag.AttributeCount = len(attrs)
	return attrs, true, diag, nil
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
	if err := c.EnsureRecoveryBin(ctx); err != nil {
		log.Warnf("eeprom: ensure recovery.bin failed: %v (staged update may not flash)", err)
	}
	out, _ := c.GetEEPROMConfig()
	out.Pending = true
	return out, nil
}

// CancelEEPROMUpdate removes any staged pieeprom.upd. Next read will show
// the live config parsed from pieeprom.bin with Pending=false.
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

// EnsureRecoveryBin makes sure recovery.bin is present on the FAT. It is
// the second piece rpi-eeprom-update needs (alongside pieeprom.upd) to
// apply a staged EEPROM update on the next boot — without it the staged
// pieeprom.upd just sits there. No-op when already present.
//
// Errors are non-fatal at the call site (warned and continued): a previous
// successful stage is sometimes enough, and we'd rather complete the .upd
// write than refuse to stage anything on a transient network failure.
func (c *Controller) EnsureRecoveryBin(ctx context.Context) error {
	if ok, _ := c.hasFileOnImage(eepromRecoveryFile); ok {
		return nil
	}
	return c.refreshRecoveryBinLocked(ctx)
}

// RefreshRecoveryBin force-downloads the latest channel recovery.bin and
// overwrites the FAT copy. Use when the bootloader release changes and
// the recovery loader needs to match.
func (c *Controller) RefreshRecoveryBin(ctx context.Context) error {
	return c.refreshRecoveryBinLocked(ctx)
}

func (c *Controller) refreshRecoveryBinLocked(ctx context.Context) error {
	if ctx == nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
	}

	img, err := eepromupdater.FindLatest(ctx, eepromupdater.FindLatestOptions{
		Platform: eepromupdater.PlatformRPi5,
		Channel:  eepromupdater.ChannelStable,
	})
	if err != nil {
		return fmt.Errorf("find latest: %w", err)
	}
	if img.RecoveryURL == "" {
		return fmt.Errorf("upstream channel %s/%s ships no recovery.bin", img.Platform, img.Channel)
	}
	log.Infof("eeprom: downloading recovery.bin (%d bytes) from %s/%s", img.RecoverySize, img.Platform, img.Channel)

	data, err := eepromupdater.DownloadRecovery(ctx, img, nil)
	if err != nil {
		return fmt.Errorf("download recovery.bin: %w", err)
	}
	if err := c.WriteFileToImage(eepromRecoveryFile, data); err != nil {
		return fmt.Errorf("write %s: %w", eepromRecoveryFile, err)
	}
	log.Infof("eeprom: wrote %s on firmware FAT (%d bytes)", eepromRecoveryFile, len(data))
	return nil
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
