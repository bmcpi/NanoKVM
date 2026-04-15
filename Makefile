# Makefile for NanoKVM BMC Project

# Configuration
IMAGE_NAME := nanokvm-builder
UID := $(shell id -u)
GID := $(shell id -g)
PWD := $(shell pwd)

.PHONY: help templ app support all clean

# Default target
all: app support

# Help target
help:
	@echo "NanoKVM BMC Build System"
	@echo ""
	@echo "Available targets:"
	@echo "  help          - Show this help message"
	@echo "  templ         - Generate Go code from templ templates"
	@echo "  app           - Build Go application server (runs templ generate first)"
	@echo "  all           - Build app and support (default)"
	@echo "  clean         - Clean build artifacts"
	@echo ""
	@echo "Prerequisites:"
	@echo "  - Docker must be installed and running"
	@echo "  - Must not run as root user"

# Generate Go code from templ templates
templ:
	@echo "Generating templ code..."
	@cd server && templ generate

out/server/NanoKVM-Server:
	@echo "Creating output directory..."
	@mkdir -p out/server
	@go mod tidy
	@CGO_ENABLED=0 GOOS=linux GOARCH=riscv64 go build -o ./out/server/NanoKVM-Server ./cmd/server

out/kvm_system/kvm_system:
	@echo "Creating kvm_system output directory..."
	@mkdir -p out/kvm_system
	@go mod tidy
	@CGO_ENABLED=0 GOOS=linux GOARCH=riscv64 go build -o ./out/kvm_system/kvm_system ./cmd/system

out/kvm_system/kvm_stream:
	@echo "Creating kvm_stream output directory..."
	@mkdir -p out/kvm_system
	@touch out/kvm_system/kvm_stream


# Build Go application (generates templ first)
app: templ clean
	@echo "Building app..."
	$(MAKE) out/server/NanoKVM-Server

# Clean build artifacts
clean:
	@echo "Cleaning build artifacts..."
	@if [ -f out/server/NanoKVM-Server ]; then \
		rm -f out/server/NanoKVM-Server; \
		echo "Removed NanoKVM-Server"; \
	fi
	@echo "Clean completed."