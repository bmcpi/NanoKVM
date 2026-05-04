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
	MountPoint    string `yaml:"mountPoint"`
	MachineEnv    string `yaml:"machineEnv"`
	PersistentEnv string `yaml:"persistentEnv"`
	OnceEnv       string `yaml:"onceEnv"`
}
