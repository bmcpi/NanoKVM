package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/BMCPi/NanoKVM/server/config"
	"github.com/BMCPi/NanoKVM/server/logger"
	"github.com/BMCPi/NanoKVM/server/middleware"
	"github.com/BMCPi/NanoKVM/server/router"
	"github.com/BMCPi/NanoKVM/server/service/application"
	"github.com/BMCPi/NanoKVM/server/service/autoupdate"
	"github.com/BMCPi/NanoKVM/server/service/firmware"
	"github.com/BMCPi/NanoKVM/server/service/ipmi"
	"github.com/BMCPi/NanoKVM/server/telemetry"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
)

// Set by goreleaser ldflags.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

var ipmiServer *ipmi.Server

func main() {
	initialize()
	defer dispose()

	run()
}

func initialize() {
	log.Printf("NanoKVM BMC %s (commit=%s, built=%s)", version, commit, date)

	// Propagate build-time version to the application service.
	application.Version = version
	telemetry.Version = version

	logger.Init()

	// Initialize OpenTelemetry + Prometheus (no-op when disabled in config).
	if err := telemetry.Init(context.Background()); err != nil {
		log.Printf("telemetry init: %v", err)
	}

	// Start IPMI server on standard port 623
	srv, err := ipmi.Start(623)
	if err != nil {
		log.Printf("IPMI server failed to start: %v", err)
	} else {
		ipmiServer = srv
	}

	// Initialize firmware controller (mount image if available).
	if err := firmware.GetController().Init(); err != nil {
		log.Printf("Firmware controller init: %v", err)
	}

	// Start the auto-update ticker (no-op when AutoUpdate.Enabled is false).
	autoupdate.Start()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT)
	go func() {
		sig := <-sigChan
		log.Printf("\nReceived signal: %v\n", sig)

		dispose()
		os.Exit(0)
	}()
}

func run() {
	conf := config.GetInstance()

	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())
	if conf.Authentication == "disable" {
		r.Use(cors.Default())
	}

	// Configure templ renderer with fallback to Gin's default HTML renderer.
	ginHtmlRenderer := r.HTMLRender
	r.HTMLRender = &router.HTMLTemplRenderer{FallbackHtmlRenderer: ginHtmlRenderer}

	router.Init(r)

	httpAddr := fmt.Sprintf(":%d", conf.Port.Http)
	httpsAddr := fmt.Sprintf(":%d", conf.Port.Https)

	if conf.Proto == "https" {
		go func() {
			err := r.RunTLS(httpsAddr, conf.Cert.Crt, conf.Cert.Key)
			if err != nil {
				panic("start https server failed")
			}
		}()

		if err := middleware.ListenAndServeLoopbackHTTPRedirect(
			httpAddr,
			httpsAddr,
			r,
		); err != nil {
			panic("start http server failed")
		}
	} else {
		if err := r.Run(httpAddr); err != nil {
			panic("start http server failed")
		}
	}
}

func dispose() {
	autoupdate.Stop()
	if ipmiServer != nil {
		ipmiServer.Stop()
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	telemetry.Shutdown(shutdownCtx)
}
