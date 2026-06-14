# Lista de comandos
#
#   make up         — gera os certs (se faltarem), copia o .env (se faltar) e sobe o sistema
#   make down       — para os containers, mantém os volumes de dados
#   make clean      — para os containers e apaga os volumes
#   make rebuild    — rebuild sem cache e sobe
#   make logs       — acompanha os logs de todos os serviços
#   make test       — roda go test ./... dentro da imagem oficial do golang
#   make certs      — gera o cert TLS (só se não existir)
#   make certs-force— sempre regera o cert TLS
#   make status     — mostra o estado dos containers
#   make dashboard  — abre o dashboard no navegador
#   make help       — mostra essa lista

.PHONY: help up down clean rebuild logs test certs certs-force status dashboard envfile

.DEFAULT_GOAL := up

COMPOSE       := docker compose
GATEWAY_URL   := https://localhost:8443

DOCKER_DESKTOP_BIN := /Applications/Docker.app/Contents/Resources/bin

ifneq (,$(wildcard $(DOCKER_DESKTOP_BIN)))
  export PATH := $(DOCKER_DESKTOP_BIN):$(PATH)
endif

help:
	@echo "Targets:"
	@echo "  up           (padrão) gera certs e .env, depois docker compose up --build"
	@echo "  down         para e remove containers (mantém volumes de dados)"
	@echo "  clean        para containers e apaga os volumes"
	@echo "  rebuild      rebuild sem cache e sobe"
	@echo "  logs         acompanha logs de todos os serviços"
	@echo "  test         roda a suíte de testes Go dentro da imagem golang:1.22-alpine"
	@echo "  certs        gera o cert TLS se não existir"
	@echo "  certs-force  sempre regera o cert TLS"
	@echo "  status       mostra o estado dos containers"
	@echo "  dashboard    abre o dashboard no navegador"

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
