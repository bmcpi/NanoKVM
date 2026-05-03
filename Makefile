# Makefile for NanoKVM BMC Project

# Configuration
IMAGE_NAME := nanokvm-builder
UID := $(shell id -u)
GID := $(shell id -g)
PWD := $(shell pwd)

.PHONY: help templ app all clean snapshot

# Default target
all: app

# Help target
help:
	@echo "NanoKVM BMC Build System"
	@echo ""
	@echo "Available targets:"
	@echo "  help          - Show this help message"
	@echo "  templ         - Generate Go code from templ templates"
	@echo "  app           - Build Go application server (runs templ generate first)"
	@echo "  fw_env        - Build fw_env CLI tool"
	@echo "  all           - Build app (default)"
	@echo "  clean         - Clean build artifacts"
	@echo "  snapshot      - Build snapshot release with goreleaser (no publish)"
	@echo ""
	@echo "Prerequisites:"
	@echo "  - Docker must be installed and running"
	@echo "  - Must not run as root user"

# Generate Go code from templ templates
generate:
	@echo "Generating code..."
	go generate ./...

dist/server/NanoKVM-Server:
	@echo "Creating output directory..."
	@mkdir -p dist/server
	@go mod tidy
	@CGO_ENABLED=0 GOOS=linux GOARCH=riscv64 go build -o ./dist/server/NanoKVM-Server ./cmd/server

dist/fw_env/fw_env:
	@echo "Creating fw_env output directory..."
	@mkdir -p dist/fw_env
	@go mod tidy
	@CGO_ENABLED=0 GOOS=linux GOARCH=riscv64 go build -o ./dist/fw_env/fw_env ./cmd/fw_env

# Build Go application (generates first)
app: generate clean
	@echo "Building app..."
	$(MAKE) dist/server/NanoKVM-Server

# Build fw_env CLI
fw_env: dist/fw_env/fw_env

# Clean build artifacts
clean:
	@echo "Cleaning build artifacts..."
	@if [ -f dist/server/NanoKVM-Server ]; then \
		rm -f dist/server/NanoKVM-Server; \
		echo "Removed NanoKVM-Server"; \
	fi
	@echo "Clean completed."

# Build snapshot release using goreleaser (no publish)
snapshot:
	@goreleaser release --snapshot --clean --skip=publish
