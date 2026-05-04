package firmware

// Boot target mappings between protocols and U-Boot env values.

// RedfishToUBoot maps Redfish BootSourceOverrideTarget values to U-Boot boot_targets.
var RedfishToUBoot = map[string]string{
	"None":      "",
	"Pxe":       "dhcp",
	"Hdd":       "mmc nvme usb",
	"Cd":        "usb0",
	"BiosSetup": "",
	"UefiHttp":  "httpboot",
}

// UBootToRedfish maps U-Boot boot_targets back to Redfish values.
var UBootToRedfish = map[string]string{
	"":             "None",
	"dhcp":         "Pxe",
	"mmc nvme usb": "Hdd",
	"usb0":         "Cd",
	"httpboot":     "UefiHttp",
}

// IPMIDeviceToUBoot maps IPMI boot device selector (bits 5:2) to U-Boot boot_targets.
// 0x20 (bits 5:2 = 1000b) = "Force boot from primary remote media" — used for UEFI HTTP.
var IPMIDeviceToUBoot = map[byte]string{
	0x00: "",             // No override
	0x04: "dhcp",         // Force PXE
	0x08: "mmc nvme usb", // Force default hard disk
	0x14: "usb0",         // Force CD/DVD
	0x18: "",             // Force BIOS Setup
	0x20: "httpboot",     // Force boot from primary remote media (UEFI HTTP)
	0x24: "mmc nvme usb", // Force primary hard disk
}

// UBootToIPMIDevice maps U-Boot boot_targets to IPMI boot device selector.
var UBootToIPMIDevice = map[string]byte{
	"":             0x00,
	"dhcp":         0x04,
	"mmc nvme usb": 0x08,
	"usb0":         0x14,
	"httpboot":     0x20,
}
