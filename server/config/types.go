package config

type Config struct {
	Proto          string   `yaml:"proto"`
	Port           Port     `yaml:"port"`
	Cert           Cert     `yaml:"cert"`
	Logger         Logger   `yaml:"logger"`
	Authentication string   `yaml:"authentication"`
	JWT            JWT      `yaml:"jwt"`
	Stun           string   `yaml:"stun"`
	Turn           Turn     `yaml:"turn"`
	Security       Security `yaml:"security"`
	IPMI           IPMI     `yaml:"ipmi"`
	Redfish        Redfish  `yaml:"redfish"`
	Serial         Serial   `yaml:"serial"`
	Firmware       Firmware `yaml:"firmware"`

	Power    Power    `yaml:"power"`
	Hardware Hardware `yaml:"-"`
}

type Logger struct {
	Level string `yaml:"level"`
	File  string `yaml:"file"`
}

type Port struct {
	Http  int `yaml:"http"`
	Https int `yaml:"https"`
}

type Cert struct {
	Crt string `yaml:"crt"`
	Key string `yaml:"key"`
}

type JWT struct {
	SecretKey            string `yaml:"secretKey"`
	RefreshTokenDuration uint64 `yaml:"refreshTokenDuration"`
	RevokeTokensOnLogout bool   `yaml:"revokeTokensOnLogout"`
}

type Turn struct {
	TurnAddr string `yaml:"turnAddr"`
	TurnUser string `yaml:"turnUser"`
	TurnCred string `yaml:"turnCred"`
}

type Security struct {
	LoginLockoutDuration int `yaml:"loginLockoutDuration"`
	LoginMaxFailures     int `yaml:"loginMaxFailures"`
}

type Hardware struct {
	Version      HWVersion `yaml:"-"`
	GPIOReset    string    `yaml:"-"`
	GPIOPower    string    `yaml:"-"`
	GPIOPowerLED string    `yaml:"-"`
	GPIOHDDLed   string    `yaml:"-"`
}

// Power holds power-control configuration.
// LegacyMode opts into direct-GPIO control (cuts power pin directly) instead of
// the default button-press simulation via the power-LED header.
type Power struct {
	LegacyMode bool `yaml:"legacyMode"`
}

type IPMI struct {
	Enabled bool `yaml:"enabled"`
	Port    int  `yaml:"port"`
}

type Redfish struct {
	Enabled bool `yaml:"enabled"`
}

type Serial struct {
	Device      string `yaml:"device"`
	BaudRate    int    `yaml:"baudRate"`
	Parity      string `yaml:"parity"`
	DataBits    int    `yaml:"dataBits"`
	StopBits    int    `yaml:"stopBits"`
	FlowControl string `yaml:"flowControl"`
}

type Firmware struct {
	ImageURL  string `yaml:"imageURL"`
	ImagePath string `yaml:"imagePath"`
	// FirmwareDir is the local directory holding the canonical FAT root files
	// (u-boot.bin, config.txt, RPi *.elf/*.dat firmware blobs, .dtb files,
	// overlays/, etc.). The boot image is built from this directory; it is
	// the source of truth, allowing each file to be versioned/edited
	// independently of the composite .img.
	FirmwareDir string `yaml:"firmwareDir"`
	// MountPoint is retained for backward-compat with existing YAML files but
	// is no longer used at runtime — env paths are derived as FAT-root names.
	MountPoint string `yaml:"mountPoint"`
	// UbootEnv is U-Boot's binary env partition file (4-byte CRC32 LE header
	// + NUL-terminated key=value entries, padded to a fixed size). U-Boot
	// reads and writes this file directly via saveenv/loadenv. The BMC reads
	// it to observe the effective environment and writes it to apply
	// persistent configuration changes.
	UbootEnv string `yaml:"ubootEnv"`
	// ImportEnv is a plain-text override file applied by U-Boot on the next
	// boot and then deleted. Use it for one-shot boot configuration changes.
	// (Renamed from onceEnv.)
	ImportEnv string `yaml:"importEnv"`
	// MediaDir is the directory where ISO images for virtual media are stored.
	MediaDir string `yaml:"mediaDir"`
}
