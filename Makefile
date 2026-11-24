VERSION ?= dev
COMMIT ?= $(shell git rev-parse HEAD)
ISSUE ?=
ENV_FILE ?= .env.local
export ENV_FILE DEBUG CONFIRM
GO_PACKAGES = ./cmd/... ./internal/... ./pkg/... ./migrations/...
LDFLAGS = -X github.com/wendaining/cloudlet/internal/version.Version=$(VERSION) -X github.com/wendaining/cloudlet/internal/version.Commit=$(COMMIT)

.PHONY: setup doctor dev dev-stop dev-status dev-logs dev-reset db-migrate db-status web-check worker-check worker-build dev-check test-e2e fmt format-check lint test test-integration generate generate-check build audit check run container-check repo-sync agent-brief

setup:
	npm ci
	go mod download
	git config --local core.hooksPath .githooks
	node scripts/dev.mjs init
	npx playwright install chromium

doctor:
	node scripts/dev.mjs doctor

dev:
	node scripts/dev.mjs start

dev-stop:
	node scripts/dev.mjs stop

dev-status:
	node scripts/dev.mjs status

dev-logs:
	node scripts/dev.mjs logs

dev-reset:
	node scripts/dev.mjs reset

db-migrate:
	node scripts/dev.mjs migrate

db-status:
	node scripts/dev.mjs db-status

web-check:
	npm run lint --workspace web
	npm run test --workspace web
	npm run build --workspace web
	node scripts/check-generated.mjs web

worker-check:
	npm run check --workspace runtimes/workers

worker-build:
	npm run build --workspace runtimes/workers

dev-check:
	node --test tests/development.test.mjs

test-e2e:
	node --test tests/web.test.mjs

fmt:
	gofmt -w cmd internal pkg migrations

format-check:
	@test -z "$$(gofmt -l cmd internal pkg migrations)" || { gofmt -l cmd internal pkg migrations; exit 1; }
	go mod tidy -diff

lint:
	go vet $(GO_PACKAGES)
	npm run lint
	go tool actionlint

test:
	go test -race -count=1 $(GO_PACKAGES)

test-integration:
	node scripts/dev.mjs test-integration

generate:
	go tool oapi-codegen --config api/oapi-codegen.yaml api/openapi.yaml
	npm run generate --workspace web

generate-check:
	node scripts/check-generated.mjs

build:
	mkdir -p bin
	go build -trimpath -ldflags '$(LDFLAGS)' -o bin/cloudlet-server ./cmd/server
	go build -trimpath -ldflags '$(LDFLAGS)' -o bin/cloudlet ./cmd/cloudlet
	go build -trimpath -o bin/cloudlet-admin ./cmd/admin

audit:
	go tool govulncheck $(GO_PACKAGES)

check: format-check lint test build audit generate-check web-check worker-check test-integration dev-check test-e2e

run:
	go run ./cmd/server -config configs/cloudlet.json

container-check:
	docker build --build-arg HTTP_PROXY --build-arg HTTPS_PROXY --build-arg NO_PROXY --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) -t cloudlet:check .
	sh scripts/smoke-container.sh cloudlet:check

repo-sync:
	npm run repo:sync

agent-brief:
	npm run agent:brief -- $(ISSUE)
