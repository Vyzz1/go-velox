SHELL := /usr/bin/env bash

.PHONY: help fmt test lint tidy check proto compose-config compose-up compose-down compose-logs compose-ps compose-restart run-api-gateway run-limiter-engine run-config-service run-sync-agent dev

GO ?= go
GOFMT ?= gofmt
GOLANGCI_LINT ?= golangci-lint
DOCKER_COMPOSE ?= docker compose
PROTOC ?= protoc

COMPOSE_FILE := docker-compose.yml
DEV_LOG_DIR := .tmp/dev

help:
	@echo "Available targets:"
	@echo "  make help                Show this help"
	@echo "  make fmt                 Run gofmt on all Go files if any exist"
	@echo "  make test                Run unit tests for all Go packages"
	@echo "  make lint                Run golangci-lint if installed"
	@echo "  make tidy                Run go mod tidy"
	@echo "  make check               Run fmt, lint, and test"
	@echo "  make proto               Generate gRPC/protobuf code if proto files exist"
	@echo "  make compose-config      Validate docker compose config"
	@echo "  make compose-up          Start local infra in background"
	@echo "  make compose-down        Stop local infra"
	@echo "  make compose-logs        Tail docker compose logs"
	@echo "  make compose-ps          Show docker compose services"
	@echo "  make compose-restart     Restart local infra"
	@echo "  make dev                 Start infra and run all available app services"
	@echo "  make run-api-gateway     Run cmd/api-gateway if present"
	@echo "  make run-limiter-engine  Run cmd/limiter-engine if present"
	@echo "  make run-config-service  Run cmd/config-service if present"
	@echo "  make run-sync-agent      Run cmd/sync-agent if present"

fmt:
	@if ! find . -type f -name '*.go' | grep -q .; then \
		echo "No Go files found; skipping gofmt."; \
		exit 0; \
	fi
	@find . -type f -name '*.go' -print0 | xargs -0 $(GOFMT) -w

test:
	@if ! find . -type f -name '*.go' | grep -q .; then \
		echo "No Go files found; skipping tests."; \
		exit 0; \
	fi
	@$(GO) test ./...

lint:
	@if ! command -v $(GOLANGCI_LINT) >/dev/null 2>&1; then \
		echo "golangci-lint not installed; skipping lint."; \
		exit 0; \
	fi
	@if ! find . -type f -name '*.go' | grep -q .; then \
		echo "No Go files found; skipping lint."; \
		exit 0; \
	fi
	@$(GOLANGCI_LINT) run ./...

tidy:
	$(GO) mod tidy

check: fmt lint test

proto:
	@if [ ! -d proto ]; then \
		echo "proto/ not found; skipping code generation."; \
		exit 0; \
	fi
	@if ! find proto -type f -name '*.proto' | grep -q .; then \
		echo "No .proto files found; skipping code generation."; \
		exit 0; \
	fi
	@if ! command -v $(PROTOC) >/dev/null 2>&1; then \
		echo "protoc not installed; skipping code generation."; \
		exit 0; \
	fi
	@$(PROTOC) -I proto --go_out=. --go-grpc_out=. $$(find proto -type f -name '*.proto')

compose-config:
	$(DOCKER_COMPOSE) -f $(COMPOSE_FILE) config

compose-up:
	$(DOCKER_COMPOSE) -f $(COMPOSE_FILE) up -d

compose-down:
	$(DOCKER_COMPOSE) -f $(COMPOSE_FILE) down

compose-logs:
	$(DOCKER_COMPOSE) -f $(COMPOSE_FILE) logs -f

compose-ps:
	$(DOCKER_COMPOSE) -f $(COMPOSE_FILE) ps

compose-restart: compose-down compose-up

dev: compose-up
	@mkdir -p $(DEV_LOG_DIR)
	@for svc in api-gateway limiter-engine config-service sync-agent; do \
		main="cmd/$$svc/main.go"; \
		log="$(DEV_LOG_DIR)/$$svc.log"; \
		if [ -f "$$main" ]; then \
			echo "Starting $$svc ..."; \
			nohup $(GO) run ./cmd/$$svc >"$$log" 2>&1 & \
			echo "$$svc logs: $$log"; \
		else \
			echo "Skipping $$svc: $$main not found."; \
		fi; \
	done

run-api-gateway:
	@if [ ! -f cmd/api-gateway/main.go ]; then \
		echo "cmd/api-gateway/main.go not found."; \
		exit 1; \
	fi
	@$(GO) run ./cmd/api-gateway

run-limiter-engine:
	@if [ ! -f cmd/limiter-engine/main.go ]; then \
		echo "cmd/limiter-engine/main.go not found."; \
		exit 1; \
	fi
	@$(GO) run ./cmd/limiter-engine

run-config-service:
	@if [ ! -f cmd/config-service/main.go ]; then \
		echo "cmd/config-service/main.go not found."; \
		exit 1; \
	fi
	@$(GO) run ./cmd/config-service

run-sync-agent:
	@if [ ! -f cmd/sync-agent/main.go ]; then \
		echo "cmd/sync-agent/main.go not found."; \
		exit 1; \
	fi
	@$(GO) run ./cmd/sync-agent
