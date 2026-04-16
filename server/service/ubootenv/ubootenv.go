package ubootenv

import (
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// DefaultEnvSize is the default total size of a U-Boot environment file (0x4000 = 16384 bytes).
const DefaultEnvSize = 0x4000

// crcSize is the size of the CRC32 header in bytes.
const crcSize = 4

// Well-known U-Boot env variable names.
const (
	VarArch          = "arch"
	VarBaudRate      = "baudrate"
	VarBoard         = "board"
	VarBoardName     = "board_name"
	VarBoardRev      = "board_rev"
	VarBoardRevision = "board_revision"
	VarBootTargets   = "boot_targets"
	VarBootCmd       = "bootcmd"
	VarBootDelay     = "bootdelay"
	VarBootMeths     = "bootmeths"
	VarCPU           = "cpu"
	VarEthAddr       = "ethaddr"
	VarUSBEthAddr    = "usbethaddr"
	VarFDTFile       = "fdtfile"
	VarSerial        = "serial#"
	VarSOC           = "soc"
	VarVendor        = "vendor"
	VarVer           = "ver"
)

// inventoryKeys are env vars extracted by GetInventory.
var inventoryKeys = []string{
	VarArch,
	VarBoard,
	VarBoardName,
	VarBoardRev,
	VarBoardRevision,
	VarBootTargets,
	VarBootMeths,
	VarCPU,
	VarEthAddr,
	VarUSBEthAddr,
	VarFDTFile,
	VarSerial,
	VarSOC,
	VarVendor,
	VarVer,
}

// Env represents a parsed U-Boot environment.
type Env struct {
	Vars map[string]string
	Size int
}

// LoadFile reads and parses a U-Boot environment from a file at the given path.
func LoadFile(path string) (*Env, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read env file: %w", err)
	}
	return Parse(data)
}

// SaveFile serializes the environment and writes it atomically to the given path.
// It writes to a temporary file in the same directory, then renames to prevent corruption.
func (e *Env) SaveFile(path string) error {
	data, err := e.Marshal()
	if err != nil {
		return fmt.Errorf("marshal env: %w", err)
	}

	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".uboot.env.*.tmp")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpName := tmp.Name()

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("sync temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("close temp file: %w", err)
	}

	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("rename temp to env: %w", err)
	}
	return nil
}

// Get returns the value of a variable and whether it exists.
func (e *Env) Get(key string) (string, bool) {
	v, ok := e.Vars[key]
	return v, ok
}

// Set sets a variable value. Creates the key if it doesn't exist.
func (e *Env) Set(key, value string) {
	e.Vars[key] = value
}

// Delete removes a variable. No-op if the key doesn't exist.
func (e *Env) Delete(key string) {
	delete(e.Vars, key)
}

// GetBootTargets returns the space-separated boot_targets as a slice.
// Returns nil if boot_targets is not set.
func (e *Env) GetBootTargets() []string {
	v, ok := e.Vars[VarBootTargets]
	if !ok || v == "" {
		return nil
	}
	return strings.Fields(v)
}

// SetBootTargets sets boot_targets from a slice of target names.
func (e *Env) SetBootTargets(targets []string) {
	if len(targets) == 0 {
		delete(e.Vars, VarBootTargets)
		return
	}
	e.Vars[VarBootTargets] = strings.Join(targets, " ")
}

// GetInventory returns a map of well-known inventory variables and their values.
// Only variables that are present in the environment are included.
func (e *Env) GetInventory() map[string]string {
	inv := make(map[string]string)
	for _, key := range inventoryKeys {
		if v, ok := e.Vars[key]; ok {
			inv[key] = v
		}
	}
	return inv
}

// Parse reads a U-Boot environment from raw bytes.
// The expected format is:
//
// [4 bytes CRC32 little-endian][data...]
//
// Data consists of null-terminated "key=value" strings.
// The end of variables is marked by a double null byte.
func Parse(data []byte) (*Env, error) {
	if len(data) < crcSize+1 {
		return nil, fmt.Errorf("data too short: %d bytes", len(data))
	}

	storedCRC := binary.LittleEndian.Uint32(data[:crcSize])
	payload := data[crcSize:]

	computedCRC := crc32.ChecksumIEEE(payload)
	if storedCRC != computedCRC {
		return nil, fmt.Errorf("CRC mismatch: stored=0x%08x computed=0x%08x", storedCRC, computedCRC)
	}

	vars := make(map[string]string)
	pos := 0
	for pos < len(payload) {
		if payload[pos] == 0 {
			break // end of environment
		}

		// Find the null terminator for this entry.
		end := pos
		for end < len(payload) && payload[end] != 0 {
			end++
		}

		entry := string(payload[pos:end])
		k, v, ok := strings.Cut(entry, "=")
		if !ok {
			return nil, fmt.Errorf("malformed entry at offset %d: %q", crcSize+pos, entry)
		}
		vars[k] = v

		pos = end + 1 // skip null terminator
	}

	return &Env{
		Vars: vars,
		Size: len(data),
	}, nil
}

// Marshal serializes the environment back to the binary format.
// Keys are sorted for deterministic output.
func (e *Env) Marshal() ([]byte, error) {
	if e.Size < crcSize+2 {
		return nil, fmt.Errorf("env size too small: %d", e.Size)
	}

	buf := make([]byte, e.Size)
	dataSize := e.Size - crcSize

	// Build the payload: sorted key=value pairs separated by null bytes.
	keys := make([]string, 0, len(e.Vars))
	for k := range e.Vars {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	pos := 0
	for _, k := range keys {
		entry := k + "=" + e.Vars[k]
		needed := len(entry) + 1 // +1 for null terminator
		if pos+needed+1 > dataSize {
			return nil, fmt.Errorf("environment data exceeds available space (%d bytes)", dataSize)
		}
		copy(buf[crcSize+pos:], entry)
		pos += len(entry)
		buf[crcSize+pos] = 0 // null terminator
		pos++
	}

	// The double-null terminator is already present since the buffer is zero-initialized.

	// Compute and store CRC32 over the data portion.
	payload := buf[crcSize:]
	checksum := crc32.ChecksumIEEE(payload)
	binary.LittleEndian.PutUint32(buf[:crcSize], checksum)

	return buf, nil
}
