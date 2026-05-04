package redfish

import (
	"net/http"
	"runtime/debug"

	"github.com/gin-gonic/gin"
)

func (s *Service) GetManagerCollection(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"@odata.type":         "#ManagerCollection.ManagerCollection",
		"@odata.id":           "/redfish/v1/Managers",
		"@odata.context":      "/redfish/v1/$metadata#ManagerCollection.ManagerCollection",
		"Name":                "Manager Collection",
		"Members@odata.count": 1,
		"Members": []gin.H{
			{"@odata.id": "/redfish/v1/Managers/1"},
		},
	})
}

func (s *Service) GetManager(c *gin.Context) {
	firmwareVersion := "1.0.0"
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		firmwareVersion = info.Main.Version
	}

	c.JSON(http.StatusOK, gin.H{
		"@odata.type":     "#Manager.v1_11_0.Manager",
		"@odata.id":       "/redfish/v1/Managers/1",
		"@odata.context":  "/redfish/v1/$metadata#Manager.Manager",
		"Id":              "1",
		"Name":            "NanoKVM BMC",
		"ManagerType":     "BMC",
		"FirmwareVersion": firmwareVersion,
		"SerialInterfaces": gin.H{
			"@odata.id": "/redfish/v1/Managers/1/SerialInterfaces",
		},
		"VirtualMedia": gin.H{
			"@odata.id": "/redfish/v1/Managers/1/VirtualMedia",
		},
		"NetworkInterfaces": gin.H{
			"@odata.id": "/redfish/v1/Managers/1/NetworkInterfaces",
		},
	})
}
