package redfish

import (
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"

	"github.com/BMCPi/NanoKVM/server/service/firmware"
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
// Body: { "Image": "<http(s) URL to ISO>" }
// The ISO is downloaded into the media staging directory and then inserted
// into the firmware FAT image as vm.iso.
func (s *Service) InsertMedia(c *gin.Context) {
	var req struct {
		Image    string `json:"Image"`    // URL of the ISO to download and insert
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

	// Validate URL — only http/https.
	parsed, err := url.ParseRequestURI(req.Image)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		redfishErrorResponse(c, http.StatusBadRequest, "Image must be an http or https URL")
		return
	}

	name := filepath.Base(parsed.Path)
	if name == "" || name == "." {
		name = "vm.iso"
	}

	fwCtrl := firmware.GetController()

	// Download the ISO into the media staging directory.
	// #nosec G107 — scheme already validated above.
	resp, err := http.Get(req.Image) //nolint:noctx
	if err != nil {
		redfishErrorResponse(c, http.StatusBadGateway, "fetch failed: "+err.Error())
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		redfishErrorResponse(c, http.StatusBadGateway, fmt.Sprintf("remote returned %d", resp.StatusCode))
		return
	}

	// For ISO sources, materialize a 1 GiB hybrid raw disk image so the
	// gadget can present it as a writable, BIOS/UEFI-bootable USB block
	// device rather than a CD-ROM. Other formats are stored verbatim.
	mediaName := name
	if _, err := fwCtrl.SaveMediaFile(name, resp.Body); err != nil {
		redfishErrorResponse(c, http.StatusInternalServerError, "save media failed: "+err.Error())
		return
	}

	// Insert the staged file into the firmware image.
	if err := fwCtrl.InsertVirtualMedia(mediaName); err != nil {
		redfishErrorResponse(c, http.StatusConflict, "insert media failed: "+err.Error())
		return
	}

	log.Infof("redfish: virtual media inserted: %s", mediaName)
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
