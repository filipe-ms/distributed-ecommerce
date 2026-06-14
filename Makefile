# Convenience targets for the distributed e-commerce stack.
#
#   make up         — generate certs (if missing), copy .env (if missing) and bring the stack up
#   make down       — stop containers, keep data volumes
#   make clean      — stop containers and wipe data volumes
#   make rebuild    — no-cache rebuild then start
#   make logs       — tail logs from every service
#   make test       — run go test ./... inside the official golang image
#   make certs      — regenerate TLS material (only if missing)
#   make certs-force— always regenerate TLS material
#   make status     — print container status
#   make dashboard  — open the monitoring dashboard in your browser
#   make help       — show this list

.PHONY: help up down clean rebuild logs test certs certs-force status dashboard envfile

.DEFAULT_GOAL := up

COMPOSE       := docker compose
GATEWAY_URL   := https://localhost:8443

# On macOS, Docker Desktop installs its credential helpers under
# /Applications/Docker.app/Contents/Resources/bin but does not always add
# that directory to the user's shell PATH. When the helper is missing,
# `docker pull` fails with "docker-credential-desktop: executable file not
# found in $PATH" even for anonymous pulls. Prepend it transparently when
# the directory exists so `make up` works without any shell setup.
DOCKER_DESKTOP_BIN := /Applications/Docker.app/Contents/Resources/bin
ifneq (,$(wildcard $(DOCKER_DESKTOP_BIN)))
  export PATH := $(DOCKER_DESKTOP_BIN):$(PATH)
endif

help:
	@echo "Targets:"
	@echo "  up           (default) generate certs and env file, then docker compose up --build"
	@echo "  down         stop and remove containers (keeps data volumes)"
	@echo "  clean        stop containers and wipe data volumes"
	@echo "  rebuild      no-cache rebuild and start"
	@echo "  logs         tail logs from every service"
	@echo "  test         run the Go test suite inside the golang:1.22-alpine image"
	@echo "  certs        regenerate TLS material if missing"
	@echo "  certs-force  always regenerate TLS material"
	@echo "  status       show container status"
	@echo "  dashboard    open the monitoring dashboard in your browser"

envfile:
	@if [ ! -f .env ]; then \
	  echo "Creating .env from .env.example"; \
	  cp .env.example .env; \
	fi

certs:
	@if [ ! -f certs/cert.pem ] || [ ! -f certs/key.pem ]; then \
	  echo "Generating self-signed certificate (valid 365 days)"; \
	  bash certs/generate.sh; \
	else \
	  echo "Certs already present in certs/ (run 'make certs-force' to regenerate)"; \
	fi

certs-force:
	bash certs/generate.sh

up: envfile certs
	$(COMPOSE) up --build

down:
	$(COMPOSE) down

clean:
	$(COMPOSE) down -v

rebuild: envfile certs
	$(COMPOSE) build --no-cache
	$(COMPOSE) up

logs:
	$(COMPOSE) logs -f

test:
	docker run --rm -v "$(CURDIR):/src" -w /src golang:1.22-alpine \
	  sh -c 'go mod tidy && go test ./...'

status:
	$(COMPOSE) ps

dashboard:
	@(open $(GATEWAY_URL)/dashboard 2>/dev/null \
	  || xdg-open $(GATEWAY_URL)/dashboard 2>/dev/null) \
	  || echo "Open $(GATEWAY_URL)/dashboard in your browser"
