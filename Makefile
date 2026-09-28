# project name
PROJECT_NAME = kcac

# Version is stamped into the binary. A tagged build reports the tag; anything
# else reports the commit, so a bug report names the exact code that produced it.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS = -s -w -X main.version=$(VERSION)

# Seeded Keycloak used by the integration tests. KC_VERSION selects the server
# major: 22.0 predates GET /groups/{id}/children and so exercises the fallback.
KC_VERSION ?= 26.0
KC_URL     ?= http://localhost:8080
KC_REALM   ?= kcac-test
KC_CLIENT  ?= kcac-audit
KC_SECRET  ?= test-secret

# Linter version, kept in step with .github/workflows/lint.yml so local runs and
# CI enforce the same rules.
GOLANGCI_VERSION ?= v2.13

# Secret scanner. The gitleaks CLI is MIT licensed and free to use; only the
# GitHub Action wrapper requires a paid licence for organisation-owned repos,
# which is why CI runs the container rather than the action.
GITLEAKS_IMAGE ?= ghcr.io/gitleaks/gitleaks:latest

# Release tooling, kept in step with .github/workflows/release.yml.
GORELEASER ?= go run github.com/goreleaser/goreleaser/v2@latest

# Prefer the Compose plugin, fall back to the standalone V1 binary.
COMPOSE := $(shell docker compose version >/dev/null 2>&1 && echo "docker compose" || echo "docker-compose") -f test/docker-compose.yml

## help: Show makefile commands.
.PHONY: help
help: Makefile
	@echo "===== Project: $(PROJECT_NAME) ($(VERSION)) ====="
	@echo
	@echo " Usage: make <COMMAND>"
	@echo
	@echo " Available Commands:"
	@echo
	@sed -n 's/^##//p' $< | column -t -s ':' |  sed -e 's/^/ /'
	@echo

## build: Build the binary.
.PHONY: build
build:
	@echo "=== Building $(PROJECT_NAME) $(VERSION)..."
	@go build -ldflags "$(LDFLAGS)" -o $(PROJECT_NAME) ./cmd/kcac

## install: Install the binary into GOBIN.
.PHONY: install
install:
	@echo "=== Installing $(PROJECT_NAME) $(VERSION)..."
	@go install -ldflags "$(LDFLAGS)" ./cmd/kcac

## test: Run unit tests with the race detector. No Keycloak needed.
.PHONY: test
test:
	@echo "=== Running unit tests with race detector..."
	go test -count=1 -race -timeout=120s ./...

## test-integration: Run integration and oracle tests against the seeded realm.
.PHONY: test-integration
test-integration:
	@echo "=== Running integration tests against $(KC_URL)..."
	@echo "    -count=1 is required: Go would otherwise reuse a cached result from"
	@echo "    a different Keycloak version and the matrix would prove nothing."
	KCAC_TEST_URL=$(KC_URL) KCAC_TEST_REALM=$(KC_REALM) \
	KCAC_TEST_CLIENT_ID=$(KC_CLIENT) KCAC_CLIENT_SECRET=$(KC_SECRET) \
	go test -tags=integration -count=1 -race -timeout=10m ./...

## test-all: Start Keycloak, run every test, then stop it.
.PHONY: test-all
test-all: kc-start test test-integration kc-stop

## cover: Report test coverage across unit and integration tests.
.PHONY: cover
cover:
	@echo "=== Measuring coverage..."
	@KCAC_TEST_URL=$(KC_URL) KCAC_TEST_REALM=$(KC_REALM) \
	KCAC_TEST_CLIENT_ID=$(KC_CLIENT) KCAC_CLIENT_SECRET=$(KC_SECRET) \
	go test -tags=integration -count=1 -covermode=atomic -coverprofile=coverage.out -timeout=10m ./internal/...
	@go tool cover -func=coverage.out | tail -1
	@echo
	@echo "    Line-by-line report:  go tool cover -html=coverage.out"

## fmt: Format the code.
.PHONY: fmt
fmt:
	@echo "=== Formatting..."
	@gofmt -w ./cmd ./internal ./test

## vet: Run go vet over normal and integration builds.
.PHONY: vet
vet:
	@echo "=== Vetting..."
	@go vet ./...
	@go vet -tags=integration ./...

## lint: Run golangci-lint using .golangci.yml.
.PHONY: lint
lint:
	@echo "=== Linting (golangci-lint $(GOLANGCI_VERSION))..."
	@go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_VERSION) run ./...

## security: Scan for known vulnerabilities reachable from this code.
.PHONY: security
security:
	@echo "=== Running govulncheck..."
	@go run golang.org/x/vuln/cmd/govulncheck@latest ./...

## secrets: Scan the repo and its history for committed credentials.
.PHONY: secrets
secrets:
	@echo "=== Scanning for committed credentials..."
	@docker run --rm -v "$(CURDIR):/repo" $(GITLEAKS_IMAGE) git /repo \
		--no-banner --redact --exit-code 1

## audit: Lint, vulnerability scan and secret scan together.
.PHONY: audit
audit: lint security secrets

## check: Everything CI enforces, minus the Keycloak matrix.
.PHONY: check
check:
	@echo "=== Checking formatting..."
	@unformatted=$$(gofmt -l ./cmd ./internal ./test); \
	if [ -n "$$unformatted" ]; then \
		echo "These files need gofmt:"; echo "$$unformatted"; exit 1; \
	fi
	@$(MAKE) --no-print-directory vet
	@$(MAKE) --no-print-directory test

## kc-start: Start the seeded Keycloak (override with KC_VERSION=22.0).
.PHONY: kc-start
kc-start:
	@echo "=== Starting Keycloak $(KC_VERSION) with the seeded realm..."
	@KC_VERSION=$(KC_VERSION) $(COMPOSE) up -d
	@./test/wait-for-keycloak.sh
	@echo
	@echo "    Admin console:  $(KC_URL)  (admin / admin)"
	@echo "    Realm:          $(KC_REALM)"
	@echo "    Read-only client: $(KC_CLIENT) / $(KC_SECRET)"
	@echo
	@echo "    Fixture credentials are public by design. Never reuse them."
	@echo

## kc-start-legacy: Start Keycloak 22, which predates the /children endpoint.
.PHONY: kc-start-legacy
kc-start-legacy:
	@$(MAKE) --no-print-directory kc-start KC_VERSION=22.0
	@echo "    This server has no GET /groups/{id}/children, so kcac falls back"
	@echo "    to the nested subGroups in /groups. Both paths must agree."
	@echo

## kc-stop: Stop Keycloak and discard its data.
.PHONY: kc-stop
kc-stop:
	@echo "=== Stopping Keycloak..."
	@$(COMPOSE) down -v

## kc-logs: Tail the Keycloak container logs.
.PHONY: kc-logs
kc-logs:
	@docker logs -f kcac-test-keycloak

## fixture: Regenerate testdata/realm-export.json from test/gen.
.PHONY: fixture
fixture:
	@echo "=== Regenerating the seeded realm..."
	@go run ./test/gen
	@echo "    Restart Keycloak to load it:  make kc-stop kc-start"

## demo: Run dump and explain against the local seeded realm.
.PHONY: demo
demo:
	@KCAC_CLIENT_SECRET=$(KC_SECRET) go run ./cmd/kcac dump --quiet \
		--url $(KC_URL) --realm $(KC_REALM) --client-id $(KC_CLIENT) \
		--manifest /tmp/kcac-manifest.json -o /tmp/kcac-access.csv
	@echo "=== kcac dump — the grant_path column is the product ==="
	@echo
	@printf '  %-10s %-16s %s\n' username entitlement grant_path
	@printf '  %-10s %-16s %s\n' ---------- ---------------- --------------------------------------------------
	@awk -F, '$$2 ~ /^(m\.huber|a\.gruber|s\.novak|d\.dual|n\.nobody)$$/ \
		{ printf "  %-10s %-16s %s\n", $$2, ($$5 == "" ? "-" : $$5), $$8 }' /tmp/kcac-access.csv
	@echo
	@echo "  a.gruber holds NO direct role assignments. A direct-mappings export"
	@echo "  reports her as having no access at all."
	@echo
	@echo "=== kcac explain d.dual — every route, not just the simplest ==="
	@echo
	@KCAC_CLIENT_SECRET=$(KC_SECRET) go run ./cmd/kcac explain --quiet \
		--url $(KC_URL) --realm $(KC_REALM) --client-id $(KC_CLIENT) d.dual
	@echo "    Full CSV: /tmp/kcac-access.csv    Manifest: /tmp/kcac-manifest.json"
	@echo

## dist: Build release artefacts locally, exactly as a release would.
.PHONY: dist
dist:
	@echo "=== Building a snapshot release into dist/..."
	@$(GORELEASER) release --snapshot --clean --skip=publish
	@echo
	@echo "    Artefacts and checksums are in dist/"

## release-check: Validate .goreleaser.yaml without building anything.
.PHONY: release-check
release-check:
	@echo "=== Checking release configuration..."
	@$(GORELEASER) check

## tidy: Tidy and verify module dependencies.
.PHONY: tidy
tidy:
	@echo "=== Tidying modules..."
	@go mod tidy
	@go mod verify

## clean: Remove build artefacts.
.PHONY: clean
clean:
	@echo "=== Cleaning..."
	@rm -rf $(PROJECT_NAME) dist coverage.out cover-*.out
