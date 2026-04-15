package router

import (
	"io/fs"
	"net/http"

	"NanoKVM-Server/assets"
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
