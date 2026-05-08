package redfish

// update_service.go implements a minimal Redfish UpdateService surface
// for the U-Boot firmware: FirmwareInventory listing the current/latest
// versions, plus a SimpleUpdate action that triggers an in-place
// download from the BMCPi/firmware-images GitHub releases.

import (
	"net/http"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"

	"github.com/BMCPi/NanoKVM/server/service/firmware"
)

// GetUpdateService returns the UpdateService root.
func (s *Service) GetUpdateService(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"@odata.type":    "#UpdateService.v1_11_0.UpdateService",
		"@odata.id":      "/redfish/v1/UpdateService",
		"@odata.context": "/redfish/v1/$metadata#UpdateService.UpdateService",
		"Id":             "UpdateService",
		"Name":           "Update Service",
		"ServiceEnabled": true,
		"FirmwareInventory": gin.H{
			"@odata.id": "/redfish/v1/UpdateService/FirmwareInventory",
		},
		"Actions": gin.H{
			"#UpdateService.SimpleUpdate": gin.H{
				"target": "/redfish/v1/UpdateService/Actions/UpdateService.SimpleUpdate",
				"TransferProtocol@Redfish.AllowableValues": []string{"HTTPS"},
			},
			"#UpdateService.StartUpdate": gin.H{
				"target": "/redfish/v1/UpdateService/Actions/UpdateService.StartUpdate",
			},
		},
	})
}

// GetFirmwareInventoryCollection returns the firmware inventory collection.
func (s *Service) GetFirmwareInventoryCollection(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"@odata.type":         "#SoftwareInventoryCollection.SoftwareInventoryCollection",
		"@odata.id":           "/redfish/v1/UpdateService/FirmwareInventory",
		"@odata.context":      "/redfish/v1/$metadata#SoftwareInventoryCollection.SoftwareInventoryCollection",
		"Name":                "Firmware Inventory Collection",
		"Members@odata.count": 1,
		"Members": []gin.H{
			{"@odata.id": "/redfish/v1/UpdateService/FirmwareInventory/BIOS"},
		},
	})
}

// GetFirmwareInventoryUBoot returns the U-Boot firmware inventory entry
// (exposed under the "BIOS" id since U-Boot serves the BIOS role here).
func (s *Service) GetFirmwareInventoryUBoot(c *gin.Context) {
	ctrl := firmware.GetController()
	info, err := ctrl.GetUBootVersionInfo()
	current := info.Current
	if current == "" {
		current = "Unknown"
	}
	resp := gin.H{
		"@odata.type":    "#SoftwareInventory.v1_8_0.SoftwareInventory",
		"@odata.id":      "/redfish/v1/UpdateService/FirmwareInventory/BIOS",
		"@odata.context": "/redfish/v1/$metadata#SoftwareInventory.SoftwareInventory",
		"Id":             "BIOS",
		"Name":           "BIOS (U-Boot)",
		"SoftwareId":     "U-Boot",
		"Version":        current,
		"Updateable":     true,
		"Status": gin.H{
			"State":  "Enabled",
			"Health": "OK",
		},
	}
	if info.Latest != "" {
		resp["Oem"] = gin.H{
			"BMCPi": gin.H{
				"LatestVersion":   info.Latest,
				"UpdateAvailable": info.UpdateAvailable,
				"AssetURL":        info.AssetURL,
			},
		}
	}
	if err != nil {
		resp["Description"] = "U-Boot bootloader firmware (latest-version lookup failed: " + err.Error() + ")"
	} else {
		resp["Description"] = "U-Boot bootloader firmware"
	}
	c.JSON(http.StatusOK, resp)
}

// SimpleUpdate triggers a u-boot update from a provided ImageURI (or the
// latest GitHub release if omitted).
func (s *Service) SimpleUpdate(c *gin.Context) {
	var req struct {
		ImageURI         string   `json:"ImageURI"`
		TransferProtocol string   `json:"TransferProtocol"`
		Targets          []string `json:"Targets"`
	}
	_ = c.ShouldBindJSON(&req) // all fields optional

	ctrl := firmware.GetController()
	if ctrl.IsDownloading() {
		redfishErrorResponse(c, http.StatusConflict, "update already in progress")
		return
	}

	go func(url string) {
		var err error
		if url != "" {
			err = ctrl.UpdateUBootFromURL(url)
		} else {
			err = ctrl.UpdateUBoot()
		}
		if err != nil {
			log.Errorf("redfish: u-boot update failed: %v", err)
		}
	}(req.ImageURI)

	c.JSON(http.StatusAccepted, gin.H{
		"@odata.type": "#Message.v1_1_0.Message",
		"MessageId":   "Update.1.0.UpdateInProgress",
		"Message":     "U-Boot update started",
	})
}

// StartUpdate is an alias for SimpleUpdate with no parameters: always
// fetches the latest release from GitHub.
func (s *Service) StartUpdate(c *gin.Context) {
	ctrl := firmware.GetController()
	if ctrl.IsDownloading() {
		redfishErrorResponse(c, http.StatusConflict, "update already in progress")
		return
	}
	go func() {
		if err := ctrl.UpdateUBoot(); err != nil {
			log.Errorf("redfish: u-boot update failed: %v", err)
		}
	}()
	c.JSON(http.StatusAccepted, gin.H{
		"@odata.type": "#Message.v1_1_0.Message",
		"MessageId":   "Update.1.0.UpdateInProgress",
		"Message":     "U-Boot update started",
	})
}
