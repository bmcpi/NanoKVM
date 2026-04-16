package redfish

import (
	"net/http"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"

	"github.com/tinkerbell-community/NanoKVM/server/service/firmware"
	"github.com/tinkerbell-community/NanoKVM/server/service/power"
)

var validBootTargets = map[string]bool{
	"None":      true,
	"Pxe":       true,
	"Hdd":       true,
	"Cd":        true,
	"BiosSetup": true,
}

func (s *Service) GetSystemCollection(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"@odata.type":    "#ComputerSystemCollection.ComputerSystemCollection",
		"@odata.id":      "/redfish/v1/Systems",
		"@odata.context": "/redfish/v1/$metadata#ComputerSystemCollection.ComputerSystemCollection",
		"Name":           "Computer System Collection",
		"Members@odata.count": 1,
		"Members": []gin.H{
			{"@odata.id": "/redfish/v1/Systems/1"},
		},
	})
}

func (s *Service) GetSystem(c *gin.Context) {
	c.JSON(http.StatusOK, buildSystemResource())
}

func (s *Service) ResetSystem(c *gin.Context) {
	var req struct {
		ResetType string `json:"ResetType"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		redfishErrorResponse(c, http.StatusBadRequest, "invalid request body")
		return
	}

	ctrl := power.GetController()
	var err error

	switch req.ResetType {
	case "On":
		err = ctrl.PowerOn()
	case "ForceOff":
		err = ctrl.PowerOff()
	case "GracefulShutdown":
		err = ctrl.PowerOff()
	case "ForceRestart", "PowerCycle":
		err = ctrl.Reset()
	default:
		redfishErrorResponse(c, http.StatusBadRequest, "invalid ResetType: "+req.ResetType)
		return
	}

	if err != nil {
		redfishErrorResponse(c, http.StatusInternalServerError, req.ResetType+" failed: "+err.Error())
		return
	}

	log.Debugf("redfish reset action: %s", req.ResetType)
	c.Status(http.StatusNoContent)
}

func (s *Service) PatchSystem(c *gin.Context) {
	var req struct {
		Boot struct {
			BootSourceOverrideTarget string `json:"BootSourceOverrideTarget"`
		} `json:"Boot"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		redfishErrorResponse(c, http.StatusBadRequest, "invalid request body")
		return
	}

	target := req.Boot.BootSourceOverrideTarget
	if !validBootTargets[target] {
		redfishErrorResponse(c, http.StatusBadRequest, "invalid BootSourceOverrideTarget: "+target)
		return
	}

	// Map Redfish target to U-Boot boot_targets and persist.
	fwCtrl := firmware.GetController()
	ubootTargets, ok := firmware.RedfishToUBoot[target]
	if !ok {
		ubootTargets = ""
	}

	if err := fwCtrl.SetBootTarget(ubootTargets); err != nil {
		log.Warnf("redfish: firmware env write failed (using in-memory fallback): %v", err)
	}

	log.Debugf("redfish boot override target set to: %s", target)
	c.JSON(http.StatusOK, buildSystemResource())
}

func buildSystemResource() gin.H {
	powerState := "Off"

	ctrl := power.GetController()
	on, err := ctrl.State()
	if err == nil && on {
		powerState = "On"
	}

	// Read boot target from firmware env, fall back to "None".
	currentTarget := "None"
	fwCtrl := firmware.GetController()
	if ubootTargets, err := fwCtrl.GetBootTarget(); err == nil {
		if rt, ok := firmware.UBootToRedfish[ubootTargets]; ok {
			currentTarget = rt
		}
	}

	// Read inventory from firmware env.
	systemInfo := gin.H{
		"@odata.type":    "#ComputerSystem.v1_13_0.ComputerSystem",
		"@odata.id":      "/redfish/v1/Systems/1",
		"@odata.context": "/redfish/v1/$metadata#ComputerSystem.ComputerSystem",
		"Id":             "1",
		"Name":           "Computer System",
		"SystemType":     "Physical",
		"PowerState":     powerState,
		"Boot": gin.H{
			"BootSourceOverrideTarget":  currentTarget,
			"BootSourceOverrideEnabled": "Once",
		},
		"Actions": gin.H{
			"#ComputerSystem.Reset": gin.H{
				"target": "/redfish/v1/Systems/1/Actions/ComputerSystem.Reset",
				"ResetType@Redfish.AllowableValues": []string{
					"On", "ForceOff", "GracefulShutdown", "ForceRestart", "PowerCycle",
				},
			},
		},
	}

	if inv, err := fwCtrl.GetInventory(); err == nil {
		if v, ok := inv["board_name"]; ok {
			systemInfo["Model"] = v
		}
		if v, ok := inv["serial#"]; ok {
			systemInfo["SerialNumber"] = v
		}
		if v, ok := inv["ethaddr"]; ok {
			systemInfo["MACAddress"] = v
		}
		if v, ok := inv["vendor"]; ok {
			systemInfo["Manufacturer"] = v
		}
		if v, ok := inv["cpu"]; ok {
			systemInfo["ProcessorSummary"] = gin.H{"Model": v}
		}
		if v, ok := inv["ver"]; ok {
			systemInfo["FirmwareVersion"] = v
		}
	}

	return systemInfo
}
