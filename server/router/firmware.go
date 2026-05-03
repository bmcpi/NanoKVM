package router

import (
	"net/http"

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

	api.POST("/mount", func(c *gin.Context) {
		if err := ctrl.Mount(); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"message": "mounted"})
	})

	api.POST("/unmount", func(c *gin.Context) {
		if err := ctrl.Unmount(); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"message": "unmounted"})
	})

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
