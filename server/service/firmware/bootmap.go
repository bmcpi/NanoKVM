package firmware

// Boot target mappings between protocols and U-Boot env values.

// RedfishToUBoot maps Redfish BootSourceOverrideTarget values to U-Boot boot_targets.
var RedfishToUBoot = map[string]string{
	"None":      "",
	"Pxe":       "pxe dhcp",
	"Hdd":       "mmc0 scsi0",
	"Cd":        "usb0",
	"BiosSetup": "",
}

// UBootToRedfish maps U-Boot boot_targets back to Redfish values.
var UBootToRedfish = map[string]string{
	"":           "None",
	"pxe dhcp":   "Pxe",
	"mmc0 scsi0": "Hdd",
	"usb0":       "Cd",
}

// IPMIDeviceToUBoot maps IPMI boot device selector (bits 5:2) to U-Boot boot_targets.
var IPMIDeviceToUBoot = map[byte]string{
	0x00: "",           // No override
	0x04: "pxe dhcp",   // Force PXE
	0x08: "mmc0 scsi0", // Force default hard disk
	0x14: "usb0",       // Force CD/DVD
	0x18: "",           // Force BIOS Setup
	0x24: "mmc0 scsi0", // Force primary hard disk
}

// UBootToIPMIDevice maps U-Boot boot_targets to IPMI boot device selector.
var UBootToIPMIDevice = map[string]byte{
	"":           0x00,
	"pxe dhcp":   0x04,
	"mmc0 scsi0": 0x08,
	"usb0":       0x14,
}
