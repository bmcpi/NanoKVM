package redfish

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

func (s *Service) GetServiceRoot(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"@odata.type":    "#ServiceRoot.v1_9_0.ServiceRoot",
		"@odata.id":      "/redfish/v1",
		"@odata.context": "/redfish/v1/$metadata#ServiceRoot.ServiceRoot",
		"Id":             "ServiceRoot",
		"Name":           "NanoKVM BMC",
		"RedfishVersion": "1.0.0",
		"Systems": gin.H{
			"@odata.id": "/redfish/v1/Systems",
		},
		"Managers": gin.H{
			"@odata.id": "/redfish/v1/Managers",
		},
		"Chassis": gin.H{
			"@odata.id": "/redfish/v1/Chassis",
		},
		"SessionService": gin.H{
			"@odata.id": "/redfish/v1/SessionService",
		},
		"UpdateService": gin.H{
			"@odata.id": "/redfish/v1/UpdateService",
		},
	})
}

// GetRedfishBase handles GET /redfish and returns the Redfish version object.
func (s *Service) GetRedfishBase(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"v1": "/redfish/v1",
	})
}
