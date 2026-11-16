VERSION ?= dev
COMMIT ?= $(shell git rev-parse HEAD)
ISSUE ?=
LDFLAGS = -X github.com/wendaining/cloudlet/internal/version.Version=$(VERSION) -X github.com/wendaining/cloudlet/internal/version.Commit=$(COMMIT)

.PHONY: setup doctor fmt format-check lint test build audit check run container-check repo-sync agent-brief

setup:
	npm ci
	go mod download
	git config --local core.hooksPath .githooks
	node scripts/dev.mjs init

doctor:
	node scripts/dev.mjs doctor

fmt:
	gofmt -w cmd internal pkg

format-check:
	@test -z "$$(gofmt -l cmd internal pkg)" || { gofmt -l cmd internal pkg; exit 1; }
	go mod tidy -diff

lint:
	go vet ./...
	npm run lint
	go tool actionlint

test:
	go test -race -count=1 ./...

build:
	mkdir -p bin
	go build -trimpath -ldflags '$(LDFLAGS)' -o bin/cloudlet-server ./cmd/server
	go build -trimpath -ldflags '$(LDFLAGS)' -o bin/cloudlet ./cmd/cloudlet

audit:
	go tool govulncheck ./...
	npm audit --audit-level=low

check: format-check lint test build audit

run:
	go run ./cmd/server -config configs/cloudlet.json

container-check:
	docker build --build-arg HTTP_PROXY --build-arg HTTPS_PROXY --build-arg NO_PROXY --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) -t cloudlet:check .
	sh scripts/smoke-container.sh cloudlet:check

repo-sync:
	npm run repo:sync

agent-brief:
	npm run agent:brief -- $(ISSUE)
