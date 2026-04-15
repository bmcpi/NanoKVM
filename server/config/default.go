package config

var defaultConfig = &Config{
	Proto: "http",
	Port: Port{
		Http:  80,
		Https: 443,
	},
	Cert: Cert{
		Crt: "server.crt",
		Key: "server.key",
	},
	Logger: Logger{
		Level: "info",
		File:  "stdout",
	},
	JWT: JWT{
		SecretKey:            "",
		RefreshTokenDuration: 2678400,
		RevokeTokensOnLogout: true,
	},
	Stun: "stun.l.google.com:19302",
	Turn: Turn{
		TurnAddr: "",
		TurnUser: "",
		TurnCred: "",
	},
	Authentication: "enable",
	Security: Security{
		LoginLockoutDuration: 0,
		LoginMaxFailures:     5,
	},
	IPMI: IPMI{
		Enabled: true,
		Port:    623,
	},
	Redfish: Redfish{
		Enabled: true,
	},
	Serial: Serial{
		Device:      "/dev/ttyS1",
		BaudRate:    115200,
		Parity:      "none",
		DataBits:    8,
		StopBits:    1,
		FlowControl: "none",
	},
}

func checkDefaultValue() {
	if instance.JWT.SecretKey == "" {
		instance.JWT.SecretKey = generateRandomSecretKey()
		instance.JWT.RevokeTokensOnLogout = true
	}

	if instance.JWT.RefreshTokenDuration == 0 {
		instance.JWT.RefreshTokenDuration = 2678400
	}

	if instance.Stun == "" {
		instance.Stun = "stun.l.google.com:19302"
	}

	if instance.Authentication == "" {
		instance.Authentication = "enable"
	}

	// Apply serial defaults when not present in the config file.
	if instance.Serial.Device == "" {
		instance.Serial.Device = defaultConfig.Serial.Device
	}
	if instance.Serial.BaudRate == 0 {
		instance.Serial.BaudRate = defaultConfig.Serial.BaudRate
	}
	if instance.Serial.Parity == "" {
		instance.Serial.Parity = defaultConfig.Serial.Parity
	}
	if instance.Serial.DataBits == 0 {
		instance.Serial.DataBits = defaultConfig.Serial.DataBits
	}
	if instance.Serial.StopBits == 0 {
		instance.Serial.StopBits = defaultConfig.Serial.StopBits
	}
	if instance.Serial.FlowControl == "" {
		instance.Serial.FlowControl = defaultConfig.Serial.FlowControl
	}

	instance.Hardware = getHardware()
}
