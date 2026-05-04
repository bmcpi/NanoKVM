package router

import (
	"fmt"
	"io"
	"net/http"
	"path/filepath"

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
		persistent, err := ctrl.GetBootTarget()
		if err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": err.Error()})
			return
		}
		once, err := ctrl.GetOnceBootTarget()
		if err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": err.Error()})
			return
		}
		effective, _ := ctrl.GetEffectiveBootTarget() // best-effort; ignore error
		c.JSON(http.StatusOK, gin.H{
			"persistent": persistent,
			"once":       once,
			"effective":  effective,
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
