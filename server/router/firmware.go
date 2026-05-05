package router

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"

	"github.com/tinkerbell-community/NanoKVM/server/middleware"
	"github.com/tinkerbell-community/NanoKVM/server/service/firmware"
)

func firmwareRouter(r *gin.Engine) {
	ctrl := firmware.GetController()

	api := r.Group("/api/firmware").Use(middleware.CheckToken())

	api.GET("/status", func(c *gin.Context) {
		c.JSON(http.StatusOK, ctrl.GetStatus())
	})

	api.POST("/download", func(c *gin.Context) {
		if ctrl.IsDownloading() {
			c.JSON(http.StatusConflict, gin.H{"error": "download already in progress"})
			return
		}

		go func() {
			if err := ctrl.DownloadAndInit(); err != nil {
				log.Errorf("firmware download failed: %v", err)
			}
		}()

		c.JSON(http.StatusAccepted, gin.H{"message": "download started"})
	})

	api.GET("/env", func(c *gin.Context) {
		vars, err := ctrl.GetAllEnvVars()
		if err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, vars)
	})

	api.GET("/inventory", func(c *gin.Context) {
		inv, err := ctrl.GetInventory()
		if err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, inv)
	})

	api.GET("/boot", func(c *gin.Context) {
		bt, err := ctrl.GetBootTargets()
		if err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"persistent": bt.Persistent,
			"once":       bt.Once,
			"effective":  bt.Effective,
		})
	})

	api.PATCH("/boot", func(c *gin.Context) {
		var req struct {
			BootTargets string `json:"boot_targets"`
			Persistence string `json:"persistence"` // "once" (default) or "continuous"
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
			return
		}

		var setErr error
		if req.Persistence == "continuous" {
			setErr = ctrl.SetBootTarget(req.BootTargets)
		} else {
			setErr = ctrl.SetBootTargetOnce(req.BootTargets)
		}
		if setErr != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": setErr.Error()})
			return
		}

		c.JSON(http.StatusOK, gin.H{"boot_targets": req.BootTargets, "persistence": req.Persistence})
	})

	// ---- file management (direct FAT I/O via go-diskfs) --------------------

	// GET /api/firmware/files — list all files in the FAT root.
	api.GET("/files", func(c *gin.Context) {
		names, err := ctrl.ListFilesInImage()
		if err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"files": names})
	})

	// GET /api/firmware/file/:name — download a file from the FAT image.
	api.GET("/file/:name", func(c *gin.Context) {
		name := filepath.Base(c.Param("name")) // sanitise; stay at root
		data, err := ctrl.ReadFileFromImage(name)
		if err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": err.Error()})
			return
		}
		if data == nil {
			c.JSON(http.StatusNotFound, gin.H{"error": fmt.Sprintf("%s not found in image", name)})
			return
		}
		c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, name))
		c.Data(http.StatusOK, "application/octet-stream", data)
	})

	// PUT /api/firmware/file/:name — upload / overwrite a file in the FAT image.
	// Accepts raw binary body (Content-Type: application/octet-stream) or
	// multipart form field "file".
	api.PUT("/file/:name", func(c *gin.Context) {
		name := filepath.Base(c.Param("name"))

		var data []byte
		ct := c.ContentType()
		if ct == "multipart/form-data" {
			fh, err := c.FormFile("file")
			if err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "multipart field 'file' required"})
				return
			}
			f, err := fh.Open()
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
			defer f.Close()
			data, err = io.ReadAll(f)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
		} else {
			var err error
			data, err = io.ReadAll(c.Request.Body)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
		}

		if err := ctrl.WriteFileToImage(name, data); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		c.JSON(http.StatusOK, gin.H{"file": name, "bytes": len(data)})
	})

	// DELETE /api/firmware/file/:name — remove a file from the FAT image.
	api.DELETE("/file/:name", func(c *gin.Context) {
		name := filepath.Base(c.Param("name"))
		if err := ctrl.RemoveFileFromImage(name); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"file": name, "deleted": true})
	})

	// POST /api/firmware/sync — copy files from firmwareDir into the mounted image.
	api.POST("/sync", func(c *gin.Context) {
		if err := ctrl.SyncFirmwareDirToImage(); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"message": "synced"})
	})

	// ---- virtual media (ISO) management ------------------------------------

	// GET /api/firmware/media — list staged ISOs and current insertion state.
	api.GET("/media", func(c *gin.Context) {
		names, err := ctrl.ListMediaFiles()
		if err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": err.Error()})
			return
		}
		vm := ctrl.GetVirtualMediaState()
		c.JSON(http.StatusOK, gin.H{
			"files":    names,
			"inserted": vm.ImageName,
			"state":    vm,
		})
	})

	// POST /api/firmware/media/upload — save an ISO to the staging directory
	// (multipart form field "file"). Does not insert; call /insert after.
	api.POST("/media/upload", func(c *gin.Context) {
		fh, err := c.FormFile("file")
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "multipart field 'file' required"})
			return
		}
		name := filepath.Base(fh.Filename)
		f, err := fh.Open()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		defer f.Close()
		n, err := ctrl.SaveMediaFile(name, f)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"file": name, "bytes": n})
	})

	// POST /api/firmware/media/fetch — download an ISO from a URL into the
	// staging directory. Body: { "url": "https://…/image.iso", "name": "…" (optional) }
	api.POST("/media/fetch", func(c *gin.Context) {
		var req struct {
			URL  string `json:"url"`
			Name string `json:"name"`
		}
		if err := c.ShouldBindJSON(&req); err != nil || req.URL == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "url required"})
			return
		}
		parsed, err := url.ParseRequestURI(req.URL)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			c.JSON(http.StatusBadRequest, gin.H{"error": "url must be http or https"})
			return
		}
		name := req.Name
		if name == "" {
			name = filepath.Base(parsed.Path)
		}
		name = filepath.Base(name)
		if name == "." || name == "" || strings.ContainsAny(name, "/\\") {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid filename derived from URL"})
			return
		}
		// #nosec G107 — URL already validated above to http/https only.
		resp, err := http.Get(req.URL) //nolint:noctx
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": "fetch failed: " + err.Error()})
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			c.JSON(http.StatusBadGateway, gin.H{"error": fmt.Sprintf("remote returned %d", resp.StatusCode)})
			return
		}
		n, err := ctrl.SaveMediaFile(name, resp.Body)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"file": name, "bytes": n})
	})

	// POST /api/firmware/media/insert — copy a staged ISO into the firmware
	// image as vm.iso and set the usb boot target.
	// Body: { "name": "alpine.iso" }
	api.POST("/media/insert", func(c *gin.Context) {
		var req struct {
			Name string `json:"name"`
		}
		if err := c.ShouldBindJSON(&req); err != nil || req.Name == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "name required"})
			return
		}
		if err := ctrl.InsertVirtualMedia(req.Name); err != nil {
			c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, ctrl.GetVirtualMediaState())
	})

	// DELETE /api/firmware/media/:name — remove a staged ISO (must not be inserted).
	api.DELETE("/media/:name", func(c *gin.Context) {
		name := filepath.Base(c.Param("name"))
		if err := ctrl.DeleteMediaFile(name); err != nil {
			c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"file": name, "deleted": true})
	})

	// POST /api/firmware/media/eject — eject virtual media and remove vm.iso
	// from firmwareDir and the FAT image.
	api.POST("/media/eject", func(c *gin.Context) {
		if err := ctrl.EjectVirtualMedia(); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, ctrl.GetVirtualMediaState())
	})

	// ---- gadget control ----------------------------------------------------

	api.POST("/present", func(c *gin.Context) {
		if err := ctrl.Present(); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"message": "presented"})
	})

	api.POST("/unpresent", func(c *gin.Context) {
		if err := ctrl.Unpresent(); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"message": "unpresented"})
	})
}
