# Makefile for NanoKVM BMC Project

# Configuration
IMAGE_NAME := nanokvm-builder
UID := $(shell id -u)
GID := $(shell id -g)
PWD := $(shell pwd)

.PHONY: help templ app all clean

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
	@echo ""
	@echo "Prerequisites:"
	@echo "  - Docker must be installed and running"
	@echo "  - Must not run as root user"

# Generate Go code from templ templates
templ:
	@echo "Generating templ code..."
	@cd server && templ generate

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

# Build Go application (generates templ first)
app: templ clean
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
