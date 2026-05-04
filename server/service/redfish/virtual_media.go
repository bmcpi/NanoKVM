package redfish

import (
	"net/http"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"

	"github.com/tinkerbell-community/NanoKVM/server/service/firmware"
)

// GetVirtualMediaCollection returns the VirtualMedia collection for Manager/1.
func (s *Service) GetVirtualMediaCollection(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"@odata.type":         "#VirtualMediaCollection.VirtualMediaCollection",
		"@odata.id":           "/redfish/v1/Managers/1/VirtualMedia",
		"@odata.context":      "/redfish/v1/$metadata#VirtualMediaCollection.VirtualMediaCollection",
		"Name":                "Virtual Media Collection",
		"Members@odata.count": 1,
		"Members": []gin.H{
			{"@odata.id": "/redfish/v1/Managers/1/VirtualMedia/1"},
		},
	})
}

// GetVirtualMedia returns the single VirtualMedia resource (slot 1).
func (s *Service) GetVirtualMedia(c *gin.Context) {
	c.JSON(http.StatusOK, buildVirtualMediaResource())
}

// InsertMedia handles POST …/VirtualMedia/1/Actions/VirtualMedia.InsertMedia.
// Body: { "Image": "<filename from /api/vm/media list>" }
func (s *Service) InsertMedia(c *gin.Context) {
	var req struct {
		Image    string `json:"Image"`    // filename of the ISO in the media dir
		UserName string `json:"UserName"` // accepted but ignored
		Password string `json:"Password"` // accepted but ignored
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		redfishErrorResponse(c, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Image == "" {
		redfishErrorResponse(c, http.StatusBadRequest, "Image is required")
		return
	}

	fwCtrl := firmware.GetController()
	if err := fwCtrl.InsertVirtualMedia(req.Image); err != nil {
		redfishErrorResponse(c, http.StatusConflict, "insert media failed: "+err.Error())
		return
	}

	log.Infof("redfish: virtual media inserted: %s", req.Image)
	c.JSON(http.StatusOK, buildVirtualMediaResource())
}

// EjectMedia handles POST …/VirtualMedia/1/Actions/VirtualMedia.EjectMedia.
func (s *Service) EjectMedia(c *gin.Context) {
	fwCtrl := firmware.GetController()
	if err := fwCtrl.EjectVirtualMedia(); err != nil {
		redfishErrorResponse(c, http.StatusInternalServerError, "eject media failed: "+err.Error())
		return
	}

	log.Info("redfish: virtual media ejected")
	c.Status(http.StatusNoContent)
}

func buildVirtualMediaResource() gin.H {
	fwCtrl := firmware.GetController()
	vm := fwCtrl.GetVirtualMediaState()

	connectedVia := []string{}
	insertedMedia := gin.H{}
	if vm.Inserted {
		connectedVia = []string{"USB"}
		insertedMedia = gin.H{
			"ImageName":     vm.ImageName,
			"CapacityBytes": vm.ImageSize,
		}
	}

	return gin.H{
		"@odata.type":    "#VirtualMedia.v1_3_0.VirtualMedia",
		"@odata.id":      "/redfish/v1/Managers/1/VirtualMedia/1",
		"@odata.context": "/redfish/v1/$metadata#VirtualMedia.VirtualMedia",
		"Id":             "1",
		"Name":           "Virtual Removable Media",
		"MediaTypes":     []string{"CD"},
		"MediaType":      "CD",
		"ConnectedVia":   connectedVia,
		"Inserted":       vm.Inserted,
		"WriteProtected": true,
		"InsertedMedia":  insertedMedia,
		"Actions": gin.H{
			"#VirtualMedia.InsertMedia": gin.H{
				"target": "/redfish/v1/Managers/1/VirtualMedia/1/Actions/VirtualMedia.InsertMedia",
			},
			"#VirtualMedia.EjectMedia": gin.H{
				"target": "/redfish/v1/Managers/1/VirtualMedia/1/Actions/VirtualMedia.EjectMedia",
			},
		},
	}
}
