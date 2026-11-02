VERSION ?= dev
DEV_DIR ?= .dev
GOARCH  ?= $(shell go env GOARCH)

.PHONY: help deps gen lint test test-db build build-web build-core build-home \
        dev-keyring dev-pki dev-art dev-core dev-web dev-web-mock dev-home

help: ## List targets
	@grep -E '^[a-z-]+:.*## ' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-12s %s\n", $$1, $$2}'

deps: ## Install Go and npm dependencies
	go mod download
	npm ci --prefix web
	npm ci --prefix home

gen: ## Regenerate code from proto/
	buf lint
	buf generate

lint: ## Lint everything
	buf lint
	go vet ./...
	npm run lint --prefix web
	npm run lint --prefix home

test: ## Run all tests (database tests are skipped)
	go test ./...
	npm test --prefix web
	npm test --prefix home

test-db: ## Run the Go tests, including those that need PostgreSQL ($(DEV_DIR)/database-url)
	VIGO_TEST_DATABASE_URL="$$(cat $(DEV_DIR)/database-url)" go test ./...

build: build-web build-core build-home ## Build all components

build-web: ## Build the web UI into the vigo-core embed directory
	npm run build --prefix web
	find internal/coreapi/webdist -mindepth 1 ! -name .keep -delete
	cp -r web/dist/. internal/coreapi/webdist/

build-core: ## Build vigo-core for Linux into bin/ (GOARCH overridable)
	CGO_ENABLED=0 GOOS=linux GOARCH=$(GOARCH) go build -trimpath \
	  -ldflags "-s -w -X main.version=$(VERSION)" -o bin/vigo-core-linux-$(GOARCH) ./cmd/vigo-core

build-home: ## Build vigo-home
	npm run build --prefix home

dev-keyring: ## Create $(DEV_DIR)/keyring.json if missing
	@mkdir -p $(DEV_DIR)
	@# The keyring goes with the data: losing it makes the encrypted values unreadable.
	@[ -f $(DEV_DIR)/keyring.json ] || { umask 077; \
	  printf '{"primary":"dev1","keys":{"dev1":"%s"}}\n' "$$(openssl rand -base64 32)" > $(DEV_DIR)/keyring.json; \
	  echo "Created $(DEV_DIR)/keyring.json"; }

dev-pki: ## Create a throwaway CA and core/homekit certificates under $(DEV_DIR)/pki
	go run ./internal/pki/pkitest/devpki -out $(DEV_DIR)/pki

dev-art: ## Seed $(DEV_DIR)/art with generated sample images (existing files are kept)
	go run ./internal/library/devart -out $(DEV_DIR)/art

dev-core: dev-keyring ## Run vigo-core against $(DEV_DIR) (ARGS='--fake-tv' for no TV)
	go run ./cmd/vigo-core --database-url-file $(DEV_DIR)/database-url \
	  --keyring-file $(DEV_DIR)/keyring.json --pki-dir $(DEV_DIR)/pki/core \
	  --source 'file:$(DEV_DIR)/art?id=dev' --cache-dir $(DEV_DIR)/cache $(ARGS)

dev-web: ## Vite dev server, proxying the API to VIGO_CORE_URL (default http://localhost:8080)
	npm run dev --prefix web -- --host 0.0.0.0

dev-web-mock: ## Vite dev server against the in-browser mock backend
	npm run dev:mock --prefix web -- --host 0.0.0.0

dev-home: ## Run vigo-home with the dev-pki certificates (ARGS='--core-url https://<host>:8443')
	npm run dev --prefix home -- $(ARGS)
