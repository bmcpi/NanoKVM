package router

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"NanoKVM-Server/gintemplrenderer"
	"NanoKVM-Server/templates"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

func Init(r *gin.Engine) {
	web(r)
	server(r)
	log.Debugf("router init done")
}

func web(r *gin.Engine) {
	execPath, err := os.Executable()
	if err != nil {
		panic("invalid executable path")
	}

	execDir := filepath.Dir(execPath)
	webPath := fmt.Sprintf("%s/web", execDir)

	// Serve static assets (favicon, etc.)
	r.StaticFile("/sipeed.ico", filepath.Join(webPath, "sipeed.ico"))

	// Root redirects to dashboard
	r.GET("/", func(c *gin.Context) {
		c.Redirect(http.StatusFound, "/dashboard")
	})

	// Server-rendered templ pages
	r.GET("/dashboard", func(c *gin.Context) {
		render := gintemplrenderer.New(c.Request.Context(), http.StatusOK, templates.DashboardPage())
		c.Render(http.StatusOK, render)
	})
	r.GET("/console", func(c *gin.Context) {
		render := gintemplrenderer.New(c.Request.Context(), http.StatusOK, templates.ConsolePage())
		c.Render(http.StatusOK, render)
	})
	r.GET("/settings", func(c *gin.Context) {
		render := gintemplrenderer.New(c.Request.Context(), http.StatusOK, templates.SettingsPage())
		c.Render(http.StatusOK, render)
	})
	r.GET("/auth/login", func(c *gin.Context) {
		render := gintemplrenderer.New(c.Request.Context(), http.StatusOK, templates.LoginPage())
		c.Render(http.StatusOK, render)
	})
	r.GET("/auth/password", func(c *gin.Context) {
		render := gintemplrenderer.New(c.Request.Context(), http.StatusOK, templates.PasswordPage())
		c.Render(http.StatusOK, render)
	})
}

func server(r *gin.Engine) {
	authRouter(r)
	applicationRouter(r)
	vmRouter(r)
	storageRouter(r)
	networkRouter(r)
	picoclawRouter(r)
	wsRouter(r)
	downloadRouter(r)
	extensionsRouter(r)
	redfishRouter(r)
}
