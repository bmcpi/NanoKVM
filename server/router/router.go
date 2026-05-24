package router

import (
	"io/fs"
	"net/http"
	"path"
	"strings"

	"github.com/BMCPi/NanoKVM/server/assets"
	"github.com/BMCPi/NanoKVM/server/config"
	"github.com/BMCPi/NanoKVM/server/middleware"
	"github.com/BMCPi/NanoKVM/server/telemetry"
	"github.com/BMCPi/NanoKVM/server/templates"
	templuiAssets "github.com/templui/templui/assets"
	templuiComponents "github.com/templui/templui/components"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	log "github.com/sirupsen/logrus"
	"go.opentelemetry.io/contrib/instrumentation/github.com/gin-gonic/gin/otelgin"
)

func Init(r *gin.Engine) {
	telemetryRoutes(r)
	web(r)
	server(r)
	log.Debugf("router init done")
}

// telemetryRoutes wires up the otelgin middleware (when enabled) and the
// Prometheus scrape endpoint. Must run before any handlers are registered
// so the middleware wraps them all.
func telemetryRoutes(r *gin.Engine) {
	tcfg := config.GetInstance().Telemetry
	if !tcfg.Enabled {
		return
	}

	r.Use(otelgin.Middleware(tcfg.ServiceName))

	if tcfg.Prometheus.Enabled {
		path := tcfg.Prometheus.Path
		if path == "" {
			path = "/metrics"
		}
		handler := promhttp.HandlerFor(telemetry.PromRegistry, promhttp.HandlerOpts{
			Registry: telemetry.PromRegistry,
		})
		r.GET(path, gin.WrapH(handler))
		log.Infof("telemetry: prometheus exposed at %s", path)
	}
}

func web(r *gin.Engine) {
	// Serve embedded static assets
	cssFS, _ := fs.Sub(assets.CSS, "css")
	jsFS, _ := fs.Sub(assets.JS, "js")
	imgFS, _ := fs.Sub(assets.Img, "img")

	r.StaticFS("/css", http.FS(cssFS))
	r.StaticFS("/js", http.FS(jsFS))
	r.StaticFS("/img", http.FS(imgFS))

	templuiRoutes(r)

	// Favicon shortcut
	r.GET("/favicon.ico", func(c *gin.Context) {
		data, err := assets.Img.ReadFile("img/favicon.ico")
		if err != nil {
			c.Status(http.StatusNotFound)
			return
		}
		c.Data(http.StatusOK, "image/x-icon", data)
	})

	// Public auth pages (no middleware)
	r.GET("/auth/login", func(c *gin.Context) {
		render := newRender(c.Request.Context(), http.StatusOK, templates.LoginPage())
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

	// All page routes resolve auth status (sets authed flag, never redirects).
	pages := r.Group("/")
	pages.Use(middleware.ResolveAuth())

	// Password reset is reachable both logged-in and as a guest.
	pages.GET("/auth/password", func(c *gin.Context) {
		render := newRender(c.Request.Context(), http.StatusOK, templates.PasswordPage(middleware.IsAuthed(c)))
		c.Render(http.StatusOK, render)
	})

	// Protected pages — require valid JWT cookie, redirect to login otherwise.
	protected := pages.Group("/")
	protected.Use(middleware.RequireAuth())

	protected.GET("/", func(c *gin.Context) {
		c.Redirect(http.StatusFound, "/dashboard")
	})
	protected.GET("/dashboard", func(c *gin.Context) {
		render := newRender(c.Request.Context(), http.StatusOK, templates.DashboardPage())
		c.Render(http.StatusOK, render)
	})
	protected.GET("/console", func(c *gin.Context) {
		render := newRender(c.Request.Context(), http.StatusOK, templates.ConsolePage())
		c.Render(http.StatusOK, render)
	})
	protected.GET("/settings", func(c *gin.Context) {
		render := newRender(c.Request.Context(), http.StatusOK, templates.SettingsPage())
		c.Render(http.StatusOK, render)
	})

	// API docs — custom templui-rendered view of the embedded OpenAPI
	// spec. The raw spec stays public at /redfish/v1/openapi.{yaml,json}
	// for tooling discovery; the rendered docs page is behind auth so
	// it shares the dashboard chrome.
	protected.GET("/docs", apiDocsHandler())
}

// apiDocsHandler parses the OpenAPI spec once (sync.Once via the model
// cache below) and renders the templates.APIDocsPage on every request.
func apiDocsHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		model, err := loadAPIDocsModel()
		if err != nil {
			log.Errorf("api docs: load model: %v", err)
			c.String(http.StatusInternalServerError, "API docs unavailable: %v", err)
			return
		}
		render := newRender(c.Request.Context(), http.StatusOK, templates.APIDocsPage(model))
		c.Render(http.StatusOK, render)
	}
}

// templuiRoutes serves templui's per-component JavaScript (loaded by the
// component Script() partials as <script src="/templui/js/<name>.min.js">)
// and templui's static assets (fonts, etc.) directly from the Go module's
// embedded filesystems. No build step is required.
func templuiRoutes(r *gin.Engine) {
	r.GET("/templui/js/*filepath", func(c *gin.Context) {
		// Path looks like "/dropdown.min.js" or "/popover.min.js".
		// templui embeds each component's JS at "<name>/<name>.min.js"
		// (or "<name>/<name>.js" for the unminified variant).
		file := strings.TrimPrefix(c.Param("filepath"), "/")
		if file == "" || strings.Contains(file, "..") {
			c.Status(http.StatusBadRequest)
			return
		}
		name := strings.TrimSuffix(strings.TrimSuffix(file, ".min.js"), ".js")
		data, err := templuiComponents.TemplFiles.ReadFile(path.Join(name, file))
		if err != nil {
			c.Status(http.StatusNotFound)
			return
		}
		c.Data(http.StatusOK, "application/javascript; charset=utf-8", data)
	})

	// templui ships fonts/images under assets/. Mount them at /templui/assets
	// so the CSS @font-face URLs (/assets/fonts/...) resolve when we expose
	// them via a rewrite below.
	r.GET("/assets/*filepath", func(c *gin.Context) {
		file := strings.TrimPrefix(c.Param("filepath"), "/")
		if file == "" || strings.Contains(file, "..") {
			c.Status(http.StatusBadRequest)
			return
		}
		data, err := templuiAssets.Assets.ReadFile(file)
		if err != nil {
			c.Status(http.StatusNotFound)
			return
		}
		// Best-effort content type — most are woff2/svg/png.
		ctype := "application/octet-stream"
		switch {
		case strings.HasSuffix(file, ".woff2"):
			ctype = "font/woff2"
		case strings.HasSuffix(file, ".woff"):
			ctype = "font/woff"
		case strings.HasSuffix(file, ".svg"):
			ctype = "image/svg+xml"
		case strings.HasSuffix(file, ".png"):
			ctype = "image/png"
		case strings.HasSuffix(file, ".css"):
			ctype = "text/css; charset=utf-8"
		case strings.HasSuffix(file, ".js"):
			ctype = "application/javascript; charset=utf-8"
		}
		c.Data(http.StatusOK, ctype, data)
	})
}

func server(r *gin.Engine) {
	authRouter(r)
	applicationRouter(r)
	vmRouter(r)
	networkRouter(r)
	redfishRouter(r)
	firmwareRouter(r)
	autoUpdateRouter(r)
}
