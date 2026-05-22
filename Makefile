BINARY      := bin/api
PKG         := ./...
LDFLAGS     := -s -w -X main.buildSHA=$(shell git rev-parse --short HEAD 2>/dev/null || echo "dev")

.PHONY: help build run test lint vet tidy clean docker-build keypair

help:
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2}'

build: ## Compile le binaire en bin/api (static, CGO off)
	CGO_ENABLED=0 go build -ldflags="$(LDFLAGS)" -o $(BINARY) ./cmd/api

run: ## Démarre l'API en local (port 8080 par défaut)
	go run ./cmd/api

test: ## Lance les tests unitaires + race detector
	go test -race -coverprofile=coverage.out $(PKG)

lint: ## golangci-lint si installé, sinon go vet
	@golangci-lint run $(PKG) 2>/dev/null || $(MAKE) vet

vet: ## go vet
	go vet $(PKG)

tidy: ## go mod tidy
	go mod tidy

clean: ## Supprime bin/ et coverage
	rm -rf bin/ coverage.out coverage.html

docker-build: ## Build l'image Docker locale (tag: pokclock-api:dev)
	docker build -t pokclock-api:dev .

keypair: ## Génère une paire RSA-2048 pour le JWT signing (dev local uniquement)
	@mkdir -p secrets-dev
	openssl genrsa -out secrets-dev/jwt_private.pem 2048
	openssl rsa -in secrets-dev/jwt_private.pem -pubout -out secrets-dev/jwt_public.pem
	@echo ""
	@echo "Paire générée dans secrets-dev/ (gitignored)."
	@echo "Pour la prod, voir docs/JWT.md."
