// Package ubootenv parses and serializes U-Boot environment files in the plain
// text format produced by `env export -t` and consumed by `env import -t`.
//
// The format is one variable per line as `key=value`. Lines may be continued
// with a trailing backslash, blank lines and `#` comments are ignored, and a
// trailing `\0` (NUL) byte that U-Boot appends is tolerated.
package ubootenv

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

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
}

// New returns an empty environment.
func New() *Env {
	return &Env{Vars: make(map[string]string)}
}

// LoadFile reads and parses a U-Boot environment text file.
func LoadFile(path string) (*Env, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read env file: %w", err)
	}
	return Parse(data)
}

// SaveFile serializes the environment and writes it atomically to the given
// path. It writes to a temporary file in the same directory, then renames to
// prevent corruption.
func (e *Env) SaveFile(path string) error {
	data := e.Marshal()

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
	if e.Vars == nil {
		e.Vars = make(map[string]string)
	}
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
// An empty slice deletes the variable.
func (e *Env) SetBootTargets(targets []string) {
	if len(targets) == 0 {
		delete(e.Vars, VarBootTargets)
		return
	}
	e.Set(VarBootTargets, strings.Join(targets, " "))
}

// GetInventory returns a map of well-known inventory variables and their
// values. Only variables that are present in the environment are included.
func (e *Env) GetInventory() map[string]string {
	inv := make(map[string]string)
	for _, key := range inventoryKeys {
		if v, ok := e.Vars[key]; ok {
			inv[key] = v
		}
	}
	return inv
}

// Parse reads a U-Boot environment from the plain-text format produced by
// `env export -t`:
//
//   - one `key=value` pair per line
//   - blank lines and lines beginning with `#` are ignored
//   - a trailing backslash continues the value on the next line (the newline
//     itself becomes part of the value)
//   - a single trailing NUL byte (appended by U-Boot in memory) is tolerated
func Parse(data []byte) (*Env, error) {
	// `env export -t` output is plain text and never contains NUL bytes.
	// In practice the file may be padded with leftover cluster slack from
	// the FAT (or U-Boot may append a single NUL terminator in memory).
	// Truncate at the first NUL so trailing garbage is ignored.
	if i := bytes.IndexByte(data, 0); i >= 0 {
		data = data[:i]
	}

	vars := make(map[string]string)
	scanner := bufio.NewScanner(bytes.NewReader(data))
	// Allow long lines (default is 64 KiB which is plenty, but be explicit).
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := scanner.Text()

		// Handle backslash continuation: a line ending in an unescaped `\`
		// joins with the following line, with a literal newline in between.
		for strings.HasSuffix(line, `\`) && !strings.HasSuffix(line, `\\`) {
			if !scanner.Scan() {
				break
			}
			lineNo++
			line = line[:len(line)-1] + "\n" + scanner.Text()
		}

		trimmed := strings.TrimLeft(line, " \t")
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}

		k, v, ok := strings.Cut(trimmed, "=")
		if !ok {
			return nil, fmt.Errorf("malformed entry on line %d: %q", lineNo, trimmed)
		}
		k = strings.TrimSpace(k)
		if k == "" {
			return nil, fmt.Errorf("empty key on line %d", lineNo)
		}
		vars[k] = v
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan env: %w", err)
	}

	return &Env{Vars: vars}, nil
}

// Marshal serializes the environment to the plain text format. Keys are
// sorted for deterministic output. Values containing newlines are emitted
// using backslash continuation so they round-trip through Parse.
func (e *Env) Marshal() []byte {
	keys := make([]string, 0, len(e.Vars))
	for k := range e.Vars {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var buf bytes.Buffer
	for _, k := range keys {
		v := e.Vars[k]
		// Escape embedded newlines as backslash-continuation so Parse
		// reconstructs the exact value.
		v = strings.ReplaceAll(v, "\n", "\\\n")
		buf.WriteString(k)
		buf.WriteByte('=')
		buf.WriteString(v)
		buf.WriteByte('\n')
	}
	return buf.Bytes()
}
