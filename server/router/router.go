package router

import (
	"io/fs"
	"net/http"

	"github.com/tinkerbell-community/NanoKVM/server/assets"
	"github.com/tinkerbell-community/NanoKVM/server/config"
	"github.com/tinkerbell-community/NanoKVM/server/gintemplrenderer"
	"github.com/tinkerbell-community/NanoKVM/server/middleware"
	"github.com/tinkerbell-community/NanoKVM/server/templates"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

func Init(r *gin.Engine) {
	web(r)
	server(r)
	log.Debugf("router init done")
}

func web(r *gin.Engine) {
	// Serve embedded static assets
	cssFS, _ := fs.Sub(assets.CSS, "css")
	jsFS, _ := fs.Sub(assets.JS, "js")
	imgFS, _ := fs.Sub(assets.Img, "img")

	r.StaticFS("/css", http.FS(cssFS))
	r.StaticFS("/js", http.FS(jsFS))
	r.StaticFS("/img", http.FS(imgFS))

	// Favicon shortcut
	r.GET("/sipeed.ico", func(c *gin.Context) {
		data, err := assets.Img.ReadFile("img/sipeed.ico")
		if err != nil {
			c.Status(http.StatusNotFound)
			return
		}
		c.Data(http.StatusOK, "image/x-icon", data)
	})

	// Public auth pages (no middleware)
	r.GET("/auth/login", func(c *gin.Context) {
		render := gintemplrenderer.New(c.Request.Context(), http.StatusOK, templates.LoginPage())
		c.Render(http.StatusOK, render)
	})

	// Token validation endpoint for client-side redirect decisions
	r.GET("/api/auth/check", func(c *gin.Context) {
		conf := config.GetInstance()
		if conf.Authentication == "disable" {
			c.JSON(http.StatusOK, gin.H{"valid": true})
			return
		}
		cookie, err := c.Cookie("nano-kvm-token")
		if err != nil || cookie == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"valid": false})
			return
		}
		if _, err := middleware.ParseJWT(cookie); err != nil {
			c.JSON(http.StatusUnauthorized, gin.H{"valid": false})
			return
		}
		c.JSON(http.StatusOK, gin.H{"valid": true})
	})

	// Protected pages — require valid JWT cookie, redirect to login otherwise
	protected := r.Group("/").Use(middleware.CheckPageAuth())

	protected.GET("/", func(c *gin.Context) {
		c.Redirect(http.StatusFound, "/dashboard")
	})
	protected.GET("/dashboard", func(c *gin.Context) {
		render := gintemplrenderer.New(c.Request.Context(), http.StatusOK, templates.DashboardPage())
		c.Render(http.StatusOK, render)
	})
	protected.GET("/console", func(c *gin.Context) {
		render := gintemplrenderer.New(c.Request.Context(), http.StatusOK, templates.ConsolePage())
		c.Render(http.StatusOK, render)
	})
	protected.GET("/settings", func(c *gin.Context) {
		render := gintemplrenderer.New(c.Request.Context(), http.StatusOK, templates.SettingsPage())
		c.Render(http.StatusOK, render)
	})
	protected.GET("/auth/password", func(c *gin.Context) {
		render := gintemplrenderer.New(c.Request.Context(), http.StatusOK, templates.PasswordPage())
		c.Render(http.StatusOK, render)
	})
}

func server(r *gin.Engine) {
	authRouter(r)
	applicationRouter(r)
	vmRouter(r)
	networkRouter(r)
	wsRouter(r)
	redfishRouter(r)
	firmwareRouter(r)
}
