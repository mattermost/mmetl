.PHONY: all build install package golangci-lint gofmt test check-style verify-gomod tidy docs docs-check

GO_PACKAGES=$(shell go list ./...)
GO ?= $(shell command -v go 2> /dev/null)
BUILD_HASH ?= $(shell git rev-parse HEAD)
BUILD_VERSION ?= $(shell git -c 'versionsort.suffix=-' ls-remote --tags --refs --sort='version:refname' https://github.com/mattermost/mmetl.git | tail -n1 | sed 's/.*\///')

LDFLAGS += -X "github.com/mattermost/mmetl/commands.BuildHash=$(BUILD_HASH)"
LDFLAGS += -X "github.com/mattermost/mmetl/commands.Version=$(BUILD_VERSION)"
BUILD_COMMAND ?= go build -ldflags '$(LDFLAGS)'
GO_TEST_FLAGS ?=

GOLANGCI_LINT_VERSION ?= 2.10.1
TOOLS_BIN := $(shell pwd)/tools/bin
GOLANGCI_LINT := $(TOOLS_BIN)/golangci-lint

UNAME_S := $(shell uname -s)
UNAME_M := $(shell uname -m)
ifeq ($(UNAME_S),Linux)
  GOLANGCI_LINT_OS := linux
else ifeq ($(UNAME_S),Darwin)
  GOLANGCI_LINT_OS := darwin
endif
ifeq ($(UNAME_M),x86_64)
  GOLANGCI_LINT_ARCH := amd64
else ifneq (,$(filter $(UNAME_M),arm64 aarch64))
  GOLANGCI_LINT_ARCH := arm64
endif

all: build

build: check-style
	$(BUILD_COMMAND)
	md5sum < mmetl | cut -d ' ' -f 1 > mmetl.md5.txt

install: check-style
	go install -ldflags '$(LDFLAGS)'

package: check-style
	mkdir -p build

	@echo Build Linux amd64
	env GOOS=linux GOARCH=amd64 $(BUILD_COMMAND)
	env GZIP=-9 tar czf build/linux_amd64.tar.gz mmetl
	md5sum < build/linux_amd64.tar.gz | cut -d ' ' -f 1 > build/linux_amd64.tar.gz.md5.txt

	@echo Build OSX amd64
	env GOOS=darwin GOARCH=amd64 $(BUILD_COMMAND)
	GZIP=-9 tar czf build/darwin_amd64.tar.gz mmetl
	md5sum < build/darwin_amd64.tar.gz | cut -d ' ' -f 1 > build/darwin_amd64.tar.gz.md5.txt

	@echo Build OSX arm64
	env GOOS=darwin GOARCH=arm64 $(BUILD_COMMAND)
	GZIP=-9 tar czf build/darwin_arm64.tar.gz mmetl
	md5sum < build/darwin_arm64.tar.gz | cut -d ' ' -f 1 > build/darwin_arm64.tar.gz.md5.txt

	@echo Build Windows amd64
	env GOOS=windows GOARCH=amd64 $(BUILD_COMMAND)
	zip -9 build/windows_amd64.zip mmetl.exe
	md5sum < build/windows_amd64.zip | cut -d ' ' -f 1 > build/windows_amd64.zip.md5.txt

	rm mmetl mmetl.exe

$(TOOLS_BIN)/golangci-lint-v$(GOLANGCI_LINT_VERSION):
	@mkdir -p $(TOOLS_BIN)
	@echo "Downloading golangci-lint v$(GOLANGCI_LINT_VERSION)..."
	@rm -f $(TOOLS_BIN)/golangci-lint-v* $(TOOLS_BIN)/golangci-lint
	@curl -sSfL "https://github.com/golangci/golangci-lint/releases/download/v$(GOLANGCI_LINT_VERSION)/golangci-lint-$(GOLANGCI_LINT_VERSION)-$(GOLANGCI_LINT_OS)-$(GOLANGCI_LINT_ARCH).tar.gz" | tar xz -C $(TOOLS_BIN) --strip-components=1 "golangci-lint-$(GOLANGCI_LINT_VERSION)-$(GOLANGCI_LINT_OS)-$(GOLANGCI_LINT_ARCH)/golangci-lint"
	@mv $(TOOLS_BIN)/golangci-lint $(TOOLS_BIN)/golangci-lint-v$(GOLANGCI_LINT_VERSION)
	@ln -sf golangci-lint-v$(GOLANGCI_LINT_VERSION) $(TOOLS_BIN)/golangci-lint

golangci-lint: $(TOOLS_BIN)/golangci-lint-v$(GOLANGCI_LINT_VERSION)
	@echo Running golangci-lint
	$(GOLANGCI_LINT) run ./...

gofmt:
	@echo Running gofmt
	@for package in $(GO_PACKAGES); do \
		echo "Checking "$$package; \
		files=$$(go list -f '{{range .GoFiles}}{{$$.Dir}}/{{.}} {{end}}' $$package); \
		if [ "$$files" ]; then \
			gofmt_output=$$(gofmt -d -s $$files 2>&1); \
			if [ "$$gofmt_output" ]; then \
				echo "$$gofmt_output"; \
				echo "Gofmt failure"; \
				exit 1; \
			fi; \
		fi; \
	done
	@echo Gofmt success

test:
	@echo Running tests
	$(GO) test -race -v $(GO_PACKAGES) -count=1 $(GO_TEST_FLAGS)

check-style: golangci-lint

verify-gomod:
	$(GO) mod download
	$(GO) mod verify

tidy:
	go mod tidy

docs:
	@echo Generating CLI documentation
	$(GO) run ./internal/tools/docgen -out ./docs/cli -frontmatter

docs-check:
	@echo Checking if docs are up-to-date
	@$(GO) run ./internal/tools/docgen -out ./docs/cli -frontmatter
	@if [ -n "$$(git status --porcelain docs/cli)" ]; then \
		echo "Documentation is out of date. Run 'make docs' to update."; \
		exit 1; \
	fi
