package ubootenv

import (
	"encoding/binary"
	"hash/crc32"
	"os"
	"path/filepath"
	"testing"
)

func makeEnv(size int, vars map[string]string) []byte {
	buf := make([]byte, size)
	pos := crcSize
	for k, v := range vars {
		entry := k + "=" + v
		copy(buf[pos:], entry)
		pos += len(entry)
		buf[pos] = 0
		pos++
	}
	crc := crc32.ChecksumIEEE(buf[crcSize:])
	binary.LittleEndian.PutUint32(buf[:crcSize], crc)
	return buf
}

func TestParseAndMarshalRoundTrip(t *testing.T) {
	original := map[string]string{
		"bootcmd":   "run distro_bootcmd",
		"bootdelay": "3",
		"ethaddr":   "00:11:22:33:44:55",
	}
	data := makeEnv(DefaultEnvSize, original)
	env, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse() error: %v", err)
	}
	if len(env.Vars) != len(original) {
		t.Fatalf("expected %d vars, got %d", len(original), len(env.Vars))
	}
	for k, want := range original {
		got, ok := env.Vars[k]
		if !ok {
			t.Errorf("missing key %q", k)
			continue
		}
		if got != want {
			t.Errorf("key %q: got %q, want %q", k, got, want)
		}
	}
	out, err := env.Marshal()
	if err != nil {
		t.Fatalf("Marshal() error: %v", err)
	}
	if len(out) != DefaultEnvSize {
		t.Fatalf("expected output size %d, got %d", DefaultEnvSize, len(out))
	}
	env2, err := Parse(out)
	if err != nil {
		t.Fatalf("Parse(round-trip) error: %v", err)
	}
	for k, want := range original {
		got := env2.Vars[k]
		if got != want {
			t.Errorf("round-trip key %q: got %q, want %q", k, got, want)
		}
	}
}

func TestParseInvalidCRC(t *testing.T) {
	data := makeEnv(DefaultEnvSize, map[string]string{"foo": "bar"})
	data[0] ^= 0xFF
	_, err := Parse(data)
	if err == nil {
		t.Fatal("expected CRC mismatch error")
	}
}

func TestParseTooShort(t *testing.T) {
	_, err := Parse([]byte{0, 0, 0})
	if err == nil {
		t.Fatal("expected error for short data")
	}
}

func TestParseEmptyEnvironment(t *testing.T) {
	data := makeEnv(DefaultEnvSize, map[string]string{})
	env, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse() error: %v", err)
	}
	if len(env.Vars) != 0 {
		t.Fatalf("expected 0 vars, got %d", len(env.Vars))
	}
}

func TestSetAndDeleteVars(t *testing.T) {
	original := map[string]string{
		"bootcmd":   "bootm",
		"bootdelay": "5",
		"toremove":  "gone",
	}
	data := makeEnv(DefaultEnvSize, original)
	env, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse() error: %v", err)
	}
	env.Vars["newvar"] = "hello"
	env.Vars["bootdelay"] = "1"
	delete(env.Vars, "toremove")
	out, err := env.Marshal()
	if err != nil {
		t.Fatalf("Marshal() error: %v", err)
	}
	env2, err := Parse(out)
	if err != nil {
		t.Fatalf("Parse(after modification) error: %v", err)
	}
	expected := map[string]string{
		"bootcmd":   "bootm",
		"bootdelay": "1",
		"newvar":    "hello",
	}
	if len(env2.Vars) != len(expected) {
		t.Fatalf("expected %d vars, got %d", len(expected), len(env2.Vars))
	}
	for k, want := range expected {
		if got := env2.Vars[k]; got != want {
			t.Errorf("key %q: got %q, want %q", k, got, want)
		}
	}
	if _, ok := env2.Vars["toremove"]; ok {
		t.Error("expected toremove to be deleted")
	}
}

func TestMarshalOverflow(t *testing.T) {
	env := &Env{
		Vars: make(map[string]string),
		Size: 20,
	}
	env.Vars["a_very_long_variable_name"] = "a_very_long_value_that_exceeds_capacity"
	_, err := env.Marshal()
	if err == nil {
		t.Fatal("expected overflow error")
	}
}

func TestGetSetDelete(t *testing.T) {
	env := &Env{Vars: map[string]string{"foo": "bar"}, Size: DefaultEnvSize}

	v, ok := env.Get("foo")
	if !ok || v != "bar" {
		t.Errorf("Get(foo): got %q, %v; want bar, true", v, ok)
	}

	_, ok = env.Get("missing")
	if ok {
		t.Error("Get(missing) should return false")
	}

	env.Set("new", "value")
	v, ok = env.Get("new")
	if !ok || v != "value" {
		t.Errorf("after Set: got %q, %v", v, ok)
	}

	env.Set("foo", "updated")
	v, _ = env.Get("foo")
	if v != "updated" {
		t.Errorf("after Set(overwrite): got %q", v)
	}

	env.Delete("foo")
	_, ok = env.Get("foo")
	if ok {
		t.Error("Delete(foo) should remove the key")
	}

	// Delete non-existent is a no-op.
	env.Delete("nonexistent")
}

func TestGetBootTargets(t *testing.T) {
	tests := []struct {
		name   string
		env    *Env
		want   []string
		wantNil bool
	}{
		{
			name:    "not set",
			env:     &Env{Vars: map[string]string{}, Size: DefaultEnvSize},
			wantNil: true,
		},
		{
			name:    "empty string",
			env:     &Env{Vars: map[string]string{VarBootTargets: ""}, Size: DefaultEnvSize},
			wantNil: true,
		},
		{
			name: "single target",
			env:  &Env{Vars: map[string]string{VarBootTargets: "mmc0"}, Size: DefaultEnvSize},
			want: []string{"mmc0"},
		},
		{
			name: "multiple targets",
			env:  &Env{Vars: map[string]string{VarBootTargets: "mmc0 usb0 pxe dhcp"}, Size: DefaultEnvSize},
			want: []string{"mmc0", "usb0", "pxe", "dhcp"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.env.GetBootTargets()
			if tt.wantNil {
				if got != nil {
					t.Errorf("expected nil, got %v", got)
				}
				return
			}
			if len(got) != len(tt.want) {
				t.Fatalf("len: got %d, want %d", len(got), len(tt.want))
			}
			for i, w := range tt.want {
				if got[i] != w {
					t.Errorf("index %d: got %q, want %q", i, got[i], w)
				}
			}
		})
	}
}

func TestSetBootTargets(t *testing.T) {
	env := &Env{Vars: map[string]string{}, Size: DefaultEnvSize}

	env.SetBootTargets([]string{"pxe", "dhcp"})
	v, ok := env.Get(VarBootTargets)
	if !ok || v != "pxe dhcp" {
		t.Errorf("after SetBootTargets: got %q, %v", v, ok)
	}

	env.SetBootTargets(nil)
	_, ok = env.Get(VarBootTargets)
	if ok {
		t.Error("SetBootTargets(nil) should delete the key")
	}

	env.SetBootTargets([]string{})
	_, ok = env.Get(VarBootTargets)
	if ok {
		t.Error("SetBootTargets([]) should delete the key")
	}
}

func TestGetInventory(t *testing.T) {
	env := &Env{
		Vars: map[string]string{
			"board_name":     "rpi",
			"board_revision": "0xE04171",
			"serial#":        "06c539f8c815f14f",
			"ethaddr":        "88:a2:9e:87:77:6b",
			"usbethaddr":     "88:a2:9e:87:77:6b",
			"fdtfile":        "broadcom/bcm2712-d-rpi-5-b.dtb",
			"arch":           "arm",
			"cpu":            "armv8",
			"soc":            "bcm283x",
			"vendor":         "raspberrypi",
			"ver":            "U-Boot 2026.04-dirty (Apr 15 2026 - 11:19:05 +0000)",
			"boot_targets":   "usb0 mmc nvme",
			"bootmeths":      "extlinux efi script pxe efi_mgr",
			"board":          "rpi",
			"board_rev":      "0x17",
			"bootcmd":        "bootflow scan -lb",
			"some_other_var": "irrelevant",
		},
		Size: DefaultEnvSize,
	}

	inv := env.GetInventory()
	// inventoryKeys has 15 entries; env has 15 of them plus 2 non-inventory vars
	if len(inv) != 15 {
		t.Fatalf("expected 15 inventory items, got %d: %v", len(inv), inv)
	}
	if inv["board_name"] != "rpi" {
		t.Errorf("board_name: got %q", inv["board_name"])
	}
	if inv["serial#"] != "06c539f8c815f14f" {
		t.Errorf("serial#: got %q", inv["serial#"])
	}
	if inv["vendor"] != "raspberrypi" {
		t.Errorf("vendor: got %q", inv["vendor"])
	}
	if inv["ver"] == "" {
		t.Error("ver should be in inventory")
	}
	if inv["cpu"] != "armv8" {
		t.Errorf("cpu: got %q", inv["cpu"])
	}
	if _, ok := inv["some_other_var"]; ok {
		t.Error("some_other_var should not be in inventory")
	}
}

func TestLoadFileAndSaveFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "uboot.env")

	// Create an env and save it.
	env := &Env{
		Vars: map[string]string{
			"bootcmd":      "run distro_bootcmd",
			"bootdelay":    "3",
			VarBootTargets: "mmc0 usb0 pxe dhcp",
			VarBoardName:   "rpi5",
		},
		Size: DefaultEnvSize,
	}

	if err := env.SaveFile(path); err != nil {
		t.Fatalf("SaveFile() error: %v", err)
	}

	// Verify the file was written with correct size.
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat after SaveFile: %v", err)
	}
	if info.Size() != int64(DefaultEnvSize) {
		t.Fatalf("file size: got %d, want %d", info.Size(), DefaultEnvSize)
	}

	// Load it back.
	loaded, err := LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile() error: %v", err)
	}
	if len(loaded.Vars) != len(env.Vars) {
		t.Fatalf("var count: got %d, want %d", len(loaded.Vars), len(env.Vars))
	}
	for k, want := range env.Vars {
		got, ok := loaded.Get(k)
		if !ok {
			t.Errorf("missing key %q after load", k)
			continue
		}
		if got != want {
			t.Errorf("key %q: got %q, want %q", k, got, want)
		}
	}

	// Modify and save again.
	loaded.Set("bootdelay", "0")
	loaded.SetBootTargets([]string{"pxe", "dhcp"})
	if err := loaded.SaveFile(path); err != nil {
		t.Fatalf("SaveFile(modified) error: %v", err)
	}

	// Load again and verify.
	loaded2, err := LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile(modified) error: %v", err)
	}
	v, _ := loaded2.Get("bootdelay")
	if v != "0" {
		t.Errorf("bootdelay after modify: got %q", v)
	}
	targets := loaded2.GetBootTargets()
	if len(targets) != 2 || targets[0] != "pxe" || targets[1] != "dhcp" {
		t.Errorf("boot_targets after modify: got %v", targets)
	}
}

func TestParseRealEnvFile(t *testing.T) {
	const envFile = "../../../data/uboot.env"
	if _, err := os.Stat(envFile); err != nil {
		t.Skipf("real env file not available: %v", err)
	}

	env, err := LoadFile(envFile)
	if err != nil {
		t.Fatalf("LoadFile(%s) error: %v", envFile, err)
	}

	if env.Size != DefaultEnvSize {
		t.Errorf("unexpected size: got %d, want %d", env.Size, DefaultEnvSize)
	}

	// Verify expected keys from a real RPi 5 U-Boot environment.
	expectedKeys := map[string]string{
		"arch":           "arm",
		"board":          "rpi",
		"board_name":     "rpi",
		"board_revision": "0xE04171",
		"boot_targets":   "usb0 mmc nvme",
		"cpu":            "armv8",
		"soc":            "bcm283x",
		"vendor":         "raspberrypi",
	}
	for key, want := range expectedKeys {
		got, ok := env.Get(key)
		if !ok {
			t.Errorf("missing key %q in real env", key)
			continue
		}
		if got != want {
			t.Errorf("key %q: got %q, want %q", key, got, want)
		}
	}

	// serial# and ethaddr should be present (values are device-specific).
	for _, key := range []string{"serial#", "ethaddr", "ver", "fdtfile"} {
		if _, ok := env.Get(key); !ok {
			t.Errorf("expected key %q in real env", key)
		}
	}

	// Round-trip: marshal and re-parse should produce identical vars.
	data, err := env.Marshal()
	if err != nil {
		t.Fatalf("Marshal() error: %v", err)
	}
	env2, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse(round-trip) error: %v", err)
	}
	if len(env2.Vars) != len(env.Vars) {
		t.Fatalf("round-trip var count: got %d, want %d", len(env2.Vars), len(env.Vars))
	}
	for k, v := range env.Vars {
		if env2.Vars[k] != v {
			t.Errorf("round-trip key %q: got %q, want %q", k, env2.Vars[k], v)
		}
	}

	// Inventory should return the expected subset.
	inv := env.GetInventory()
	if len(inv) == 0 {
		t.Fatal("GetInventory() returned empty map for real env")
	}
	t.Logf("parsed %d vars, inventory %d items from real env", len(env.Vars), len(inv))
}

func TestLoadFileNotFound(t *testing.T) {
	_, err := LoadFile("/nonexistent/path/uboot.env")
	if err == nil {
		t.Fatal("expected error for nonexistent file")
	}
}

func TestSaveFileAtomicity(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "uboot.env")

	// Write initial env.
	env := &Env{Vars: map[string]string{"a": "1"}, Size: DefaultEnvSize}
	if err := env.SaveFile(path); err != nil {
		t.Fatalf("initial SaveFile: %v", err)
	}

	// Verify no temp files remain.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.Name() != "uboot.env" {
			t.Errorf("unexpected file in dir: %s", e.Name())
		}
	}
}
