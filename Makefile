# Colors for terminal output
GREEN  := \033[1;32m
YELLOW := \033[1;33m
BLUE   := \033[1;34m
CYAN   := \033[1;36m
WHITE  := \033[1;37m
RESET  := \033[0m

# Default target - show help
.DEFAULT_GOAL := help

# Variables
# Git version information
GIT_BRANCH := $(shell git rev-parse --abbrev-ref HEAD 2>/dev/null || echo "unknown")
GIT_SHA := $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
VERSION ?= "dev/$(GIT_BRANCH)/$(GIT_SHA)"
BUILD_TIME := $(shell date -u '+%Y-%m-%d_%H:%M:%S')
LDFLAGS := -X main.Version="$(VERSION)" -X main.BuildTime="$(BUILD_TIME)" -X main.GitCommit="$(GIT_SHA)"
BINARY_NAME := blacksmith
GO_FILES := $(shell find . -name '*.go' -type f -not -path "./vendor/*")

##@ General

.PHONY: help
help: ## Display this help message
	@echo "$(BLUE)Blacksmith Makefile$(RESET)"
	@echo ""
	@awk 'BEGIN {FS = ":.*##"; printf "Usage:\n  make $(CYAN)<target>$(RESET)\n"} /^[a-zA-Z_-]+:.*?##/ { printf "  $(CYAN)%-20s$(RESET) %s\n", $$1, $$2 } /^##@/ { printf "\n$(YELLOW)%s$(RESET)\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

##@ Development

.PHONY: build
build: ## Build the blacksmith binary for current OS/architecture
	@echo "$(GREEN)Building $(BINARY_NAME)...$(RESET)"
	@go build -ldflags="$(LDFLAGS)" -o $(BINARY_NAME) .
	@echo "$(GREEN)✓ Build complete$(RESET)"

.PHONY: linux
linux: ## Build the blacksmith binary for Linux AMD64
	@echo "$(GREEN)Building $(BINARY_NAME) for Linux AMD64...$(RESET)"
	@env GOOS=linux GOARCH=amd64 go build -ldflags="$(LDFLAGS)" -o $(BINARY_NAME)-linux-amd64 .
	@echo "$(GREEN)✓ Linux build complete$(RESET)"

.PHONY: dev
dev: linux ## Build for Linux and run test deployment
	@echo "$(GREEN)Running development test...$(RESET)"
	@./bin/testdev

##@ Testing & Quality

.PHONY: test
test: ## Run all tests
	@echo "$(GREEN)Running tests...$(RESET)"
	@go test -v $(shell go list ./... | grep -v vendor)
	@echo "$(GREEN)✓ Tests complete$(RESET)"

.PHONY: test-short
test-short: ## Run tests in short mode
	@echo "$(GREEN)Running short tests...$(RESET)"
	@go test -short $(shell go list ./... | grep -v vendor)
	@echo "$(GREEN)✓ Short tests complete$(RESET)"

.PHONY: coverage
coverage: ## Generate test coverage report
	@echo "$(GREEN)Generating coverage report...$(RESET)"
	@go test -coverprofile=coverage.out $(shell go list ./... | grep -v vendor)
	@go tool cover -func=coverage.out
	@echo "$(GREEN)✓ Coverage report generated$(RESET)"

.PHONY: coverage-html
coverage-html: coverage ## Generate and open HTML coverage report
	@echo "$(GREEN)Opening HTML coverage report...$(RESET)"
	@go tool cover -html=coverage.out

.PHONY: test-all
test-all: test coverage ## Run all tests and generate coverage report
	@echo "$(GREEN)✓ All tests and coverage complete$(RESET)"

.PHONY: report
report: coverage-html ## Alias for coverage-html (backwards compatibility)

##@ Code Quality

.PHONY: fmt
fmt: ## Format all Go source files
	@echo "$(GREEN)Formatting code...$(RESET)"
	@go fmt $(shell go list ./... | grep -v vendor)
	@echo "$(GREEN)✓ Code formatted$(RESET)"

.PHONY: vet
vet: ## Run go vet on all source files
	@echo "$(GREEN)Running go vet...$(RESET)"
	@go vet $(shell go list ./... | grep -v vendor)
	@echo "$(GREEN)✓ Vet analysis complete$(RESET)"

.PHONY: lint
lint: fmt vet ## Run fmt and vet

.PHONY: govulncheck
govulncheck: ## Run vulnerability check on dependencies
	@echo "$(GREEN)Checking for vulnerabilities...$(RESET)"
	@command -v govulncheck >/dev/null 2>&1 || { \
		echo "$(YELLOW)Installing govulncheck...$(RESET)"; \
		go install golang.org/x/vuln/cmd/govulncheck@latest; \
	}
	@govulncheck $(shell go list ./... | grep -v vendor)
	@echo "$(GREEN)✓ Vulnerability check complete$(RESET)"

.PHONY: gosec
gosec: ## Run security scanner on source code
	@echo "$(GREEN)Running security scan...$(RESET)"
	@command -v gosec >/dev/null 2>&1 || { \
		echo "$(YELLOW)Installing gosec...$(RESET)"; \
		go install github.com/securego/gosec/v2/cmd/gosec@latest; \
	}
	@gosec -fmt text $(shell go list ./... | grep -v vendor)
	@echo "$(GREEN)✓ Security scan complete$(RESET)"

.PHONY: staticcheck
staticcheck: ## Run staticcheck static analysis
	@echo "$(GREEN)Running staticcheck...$(RESET)"
	@command -v staticcheck >/dev/null 2>&1 || { \
		echo "$(YELLOW)Installing staticcheck...$(RESET)"; \
		go install honnef.co/go/tools/cmd/staticcheck@latest; \
	}
	@staticcheck $(shell go list ./... | grep -v vendor)
	@echo "$(GREEN)✓ Staticcheck analysis complete$(RESET)"

.PHONY: trivy
trivy: ## Run Trivy container and dependency scanner
	@echo "$(GREEN)Running Trivy scan...$(RESET)"
	@command -v trivy >/dev/null 2>&1 || { \
		echo "$(YELLOW)Trivy not found. Please install it:$(RESET)"; \
		echo "$(CYAN)  brew install trivy$(RESET) (macOS)"; \
		echo "$(CYAN)  apt-get install trivy$(RESET) (Debian/Ubuntu)"; \
		echo "$(CYAN)  Or visit: https://aquasecurity.github.io/trivy$(RESET)"; \
		exit 1; \
	}
	@trivy fs --scanners vuln,misconfig,secret --severity HIGH,CRITICAL --skip-dirs vendor .
	@echo "$(GREEN)✓ Trivy scan complete$(RESET)"

.PHONY: security
security: govulncheck gosec trivy ## Run all security scans (govulncheck, gosec, trivy)
	@echo "$(GREEN)✓ All security scans complete$(RESET)"

.PHONY: check
check: lint vet staticcheck test ## Run basic checks (lint, vet, staticcheck, test)
	@echo "$(GREEN)✓ Basic checks passed$(RESET)"

.PHONY: check-all
check-all: lint vet test-all ## Run all checks (lint, vet, tests with coverage)
	@echo "$(GREEN)✓ All checks passed$(RESET)"

##@ Cleanup

.PHONY: clean
clean: ## Clean build artifacts and test cache
	@echo "$(YELLOW)Cleaning up...$(RESET)"
	@rm -f $(BINARY_NAME) $(BINARY_NAME)-*
	@rm -f coverage.out coverage.html test.cov
	@rm -rf artifacts/
	@rm -rf blacksmith-*/
	@go clean -testcache
	@echo "$(GREEN)✓ Cleanup complete$(RESET)"

##@ Release

.PHONY: shipit
shipit: ## Build release artifacts (requires VERSION env var)
	@echo "$(BLUE)Preparing release...$(RESET)"
	@echo "Checking that VERSION was defined in the calling environment"
	@test -n "$(VERSION)" || { echo "$(RED)ERROR: VERSION not set$(RESET)"; exit 1; }
	@echo "$(GREEN)OK. VERSION=$(VERSION)$(RESET)"

	@echo "$(GREEN)Compiling Blacksmith Broker binaries...$(RESET)"
	@rm -rf artifacts
	@mkdir artifacts
	@GOOS=darwin GOARCH=arm64 go build -ldflags="$(LDFLAGS)" -o artifacts/blacksmith-darwin-arm64 .
	@GOOS=darwin GOARCH=amd64 go build -ldflags="$(LDFLAGS)" -o artifacts/blacksmith-darwin-amd64 .
	@GOOS=linux  GOARCH=amd64 go build -ldflags="$(LDFLAGS)" -o artifacts/blacksmith-linux-amd64  .

	@echo "$(GREEN)Assembling Linux Server Distribution...$(RESET)"
	@rm -f artifacts/*.tar.gz
	@rm -rf blacksmith-$(VERSION)
	@mkdir -p blacksmith-$(VERSION)
	@cp artifacts/blacksmith-linux-amd64 blacksmith-$(VERSION)/blacksmith
	@cp -a ui blacksmith-$(VERSION)
	@tar -cjf artifacts/blacksmith-$(VERSION).tar.bz2 blacksmith-$(VERSION)/
	@rm -rf blacksmith-$(VERSION)
	@echo "$(GREEN)✓ Release artifacts built successfully$(RESET)"

.PHONY: version
version: ## Display the current version
	@echo "$(CYAN)Version: $(VERSION)$(RESET)"

##@ Deployment

.PHONY: deploy-ui
deploy-ui: ## Deploy UI files to BOSH instance (use DEPLOYMENT=name INSTANCE=group/id)
	@echo "$(GREEN)Deploying UI files to BOSH...$(RESET)"
	@./deploy-ui-quick.sh $(DEPLOYMENT) $(INSTANCE)
	@echo "$(GREEN)✓ UI deployed$(RESET)"

.PHONY: deploy-all
deploy-all: build deploy-ui ## Build and deploy both binary and UI to BOSH
	@echo "$(GREEN)Deploying blacksmith binary...$(RESET)"
	@bosh -d $(DEPLOYMENT) scp blacksmith $(INSTANCE):/tmp/blacksmith
	@bosh -d $(DEPLOYMENT) ssh $(INSTANCE) -c "sudo cp /tmp/blacksmith /var/vcap/packages/blacksmith/bin/blacksmith && sudo chown vcap:vcap /var/vcap/packages/blacksmith/bin/blacksmith && sudo chmod +x /var/vcap/packages/blacksmith/bin/blacksmith"
	@bosh -d $(DEPLOYMENT) ssh $(INSTANCE) -c "sudo /var/vcap/bosh/bin/monit restart blacksmith"
	@echo "$(GREEN)✓ Full deployment complete$(RESET)"

##@ Dependencies

.PHONY: deps
deps: ## Download and verify dependencies
	@echo "$(GREEN)Downloading dependencies...$(RESET)"
	@go mod download
	@go mod verify
	@echo "$(GREEN)✓ Dependencies ready$(RESET)"

.PHONY: deps-update
deps-update: ## Update all dependencies to latest versions
	@echo "$(GREEN)Updating dependencies...$(RESET)"
	@go get -u ./...
	@go mod tidy
	@echo "$(GREEN)✓ Dependencies updated$(RESET)"

.PHONY: deps-tidy
deps-tidy: ## Clean up go.mod and go.sum
	@echo "$(GREEN)Tidying dependencies...$(RESET)"
	@go mod tidy
	@echo "$(GREEN)✓ Dependencies tidied$(RESET)"

# Include all phony targets
.PHONY: build linux dev test test-short test-all coverage coverage-html report fmt vet lint \
        govulncheck gosec staticcheck trivy security check check-all clean shipit version deps deps-update deps-tidy help
