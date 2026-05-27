// Package eepromupdater is the BMC-side analogue of rpi-eeprom-update: it
// finds and downloads the latest published pieeprom-*.bin from upstream
// (raspberrypi/rpi-eeprom on GitHub) so the BMC can stage it on the
// shared USB FAT for the host's rpi-eeprom-update to flash on next boot.
//
// We do NOT flash the EEPROM ourselves. The BMC runs out-of-band of the
// host OS and only manages the FAT volume both ends share over the USB
// gadget.
//
// Upstream layout (on the rpi-eeprom repo's default branch):
//
//	firmware-2712/<channel>/pieeprom-YYYY-MM-DD.bin   ← RPi 5 (BCM2712)
//	firmware-2712/<channel>/recovery.bin              ← shared recovery loader
//	firmware-2711/<channel>/pieeprom-YYYY-MM-DD.bin   ← RPi 4 (BCM2711)
//	firmware-2711/<channel>/recovery.bin
//
// where <channel> is "stable", "beta", or "critical". We list the channel
// directory via the GitHub Contents API and pick the lexicographically
// largest pieeprom-*.bin (date in filename, ISO 8601 — sorts correctly).
// The recovery.bin from the same channel is fetched alongside — without
// it on the boot FAT, rpi-eeprom-update can't actually apply pieeprom.upd
// at next boot.
package eepromupdater

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

// Platform identifies which SoC's EEPROM image directory we look in.
type Platform string

const (
	PlatformRPi5 Platform = "2712" // BCM2712 / RPi 5
	PlatformRPi4 Platform = "2711" // BCM2711 / RPi 4
)

// Channel matches rpi-eeprom's release channels. Default is stable: it's
// what rpi-eeprom-update uses out of the box and is the right choice for
// hands-off updates from a BMC.
type Channel string

const (
	ChannelStable   Channel = "stable"
	ChannelBeta     Channel = "beta"
	ChannelCritical Channel = "critical"
)

// Repo / branch the EEPROM binaries are published from.
const (
	upstreamOwner  = "raspberrypi"
	upstreamRepo   = "rpi-eeprom"
	upstreamBranch = "master"
)

// Image describes one published EEPROM binary on a channel.
type Image struct {
	Name     string    `json:"name"`     // e.g. "pieeprom-2024-12-04.bin"
	URL      string    `json:"url"`      // raw GitHub download URL
	Size     int64     `json:"size"`     // bytes
	Version  string    `json:"version"`  // "2024-12-04" parsed from Name
	Platform Platform  `json:"platform"` // 2712 or 2711
	Channel  Channel   `json:"channel"`  // stable/beta/critical
	FoundAt  time.Time `json:"foundAt"`  // when the lookup happened
	// RecoveryURL / RecoverySize point at the recovery.bin shipped in the
	// same channel directory. rpi-eeprom-update needs recovery.bin sitting
	// next to pieeprom.upd on the boot FAT in order to actually apply a
	// staged update on next boot. Empty if the channel didn't carry one.
	RecoveryURL  string `json:"recoveryUrl,omitempty"`
	RecoverySize int64  `json:"recoverySize,omitempty"`
}

// FindLatestOptions tunes FindLatest. Zero-value defaults are safe (RPi 5
// stable, default HTTP client + 15s timeout).
type FindLatestOptions struct {
	Platform   Platform
	Channel    Channel
	HTTPClient *http.Client
}

// FindLatest queries the GitHub Contents API for the most recent
// pieeprom-*.bin in (platform, channel) and returns its name + download
// URL + size. Returns the most recent by filename (ISO 8601 dates sort
// lexicographically).
//
// Network call: hits api.github.com without auth. Subject to GitHub's
// unauthenticated rate limits (60 req/hr/IP). Callers should cache the
// result rather than calling per UI refresh.
func FindLatest(ctx context.Context, opts FindLatestOptions) (*Image, error) {
	platform := opts.Platform
	if platform == "" {
		platform = PlatformRPi5
	}
	channel := opts.Channel
	if channel == "" {
		channel = ChannelStable
	}
	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}

	dir := fmt.Sprintf("firmware-%s/%s", platform, channel)
	apiURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/contents/%s?ref=%s",
		upstreamOwner, upstreamRepo, dir, upstreamBranch)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("list %s: %w", dir, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("list %s: HTTP %d: %s", dir, resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var entries []listEntry
	if err := json.NewDecoder(resp.Body).Decode(&entries); err != nil {
		return nil, fmt.Errorf("decode listing: %w", err)
	}

	// Filter to pieeprom-*.bin and sort descending by name. Filenames are
	// pieeprom-YYYY-MM-DD.bin so lexical descending == newest first.
	// Capture recovery.bin in passing — same channel, same listing.
	var (
		candidates   []listEntry
		recoveryURL  string
		recoverySize int64
	)
	for _, e := range entries {
		if e.Type != "file" || e.DownloadURL == "" {
			continue
		}
		switch {
		case strings.HasPrefix(e.Name, "pieeprom-") && strings.HasSuffix(e.Name, ".bin"):
			candidates = append(candidates, listEntry{Name: e.Name, DownloadURL: e.DownloadURL, Size: e.Size})
		case e.Name == "recovery.bin":
			recoveryURL = e.DownloadURL
			recoverySize = e.Size
		}
	}
	if len(candidates) == 0 {
		return nil, fmt.Errorf("no pieeprom-*.bin entries found in %s", dir)
	}
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].Name > candidates[j].Name
	})
	top := candidates[0]
	return &Image{
		Name:         top.Name,
		URL:          top.DownloadURL,
		Size:         top.Size,
		Version:      parseVersionFromName(top.Name),
		Platform:     platform,
		Channel:      channel,
		FoundAt:      time.Now(),
		RecoveryURL:  recoveryURL,
		RecoverySize: recoverySize,
	}, nil
}

// listEntry is the subset of github contents-API fields we use.
type listEntry struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Size        int64  `json:"size"`
	DownloadURL string `json:"download_url"`
}

// parseVersionFromName turns "pieeprom-2024-12-04.bin" into "2024-12-04".
// Returns the filename unchanged when it doesn't match the expected shape.
func parseVersionFromName(name string) string {
	s := strings.TrimPrefix(name, "pieeprom-")
	s = strings.TrimSuffix(s, ".bin")
	return s
}

// Download fetches img.URL and returns the bytes. Verifies the response
// size matches img.Size when img.Size > 0. Subject to ctx cancellation.
func Download(ctx context.Context, img *Image, client *http.Client) ([]byte, error) {
	if img == nil || img.URL == "" {
		return nil, errors.New("nil image or empty URL")
	}
	return fetch(ctx, client, img.URL, img.Name, img.Size)
}

// DownloadRecovery fetches the recovery.bin associated with img (same
// channel directory). Returns an error if img has no recorded
// RecoveryURL — callers should treat that as "channel doesn't ship one"
// and skip the recovery-staging step.
func DownloadRecovery(ctx context.Context, img *Image, client *http.Client) ([]byte, error) {
	if img == nil || img.RecoveryURL == "" {
		return nil, errors.New("nil image or empty recovery URL")
	}
	return fetch(ctx, client, img.RecoveryURL, "recovery.bin", img.RecoverySize)
}

// fetch is the shared HTTP-get for Download/DownloadRecovery: same size
// cap, same context handling, same client default.
func fetch(ctx context.Context, client *http.Client, url, name string, expectedSize int64) ([]byte, error) {
	if client == nil {
		// EEPROM binaries are ~2 MB and recovery.bin is similar; allow
		// generous time for slow links.
		client = &http.Client{Timeout: 2 * time.Minute}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("get %s: %w", name, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("get %s: HTTP %d", name, resp.StatusCode)
	}

	// Cap the read so a misbehaving server can't OOM us. Real images are
	// 512 KB or 2 MB; allow up to 4 MB headroom.
	const maxBytes = 4 * 1024 * 1024
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", name, err)
	}
	if len(body) > maxBytes {
		return nil, fmt.Errorf("%s exceeded max size %d bytes", name, maxBytes)
	}
	if expectedSize > 0 && int64(len(body)) != expectedSize {
		return nil, fmt.Errorf("%s size mismatch: got %d, expected %d", name, len(body), expectedSize)
	}
	return body, nil
}
