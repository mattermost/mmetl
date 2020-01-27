.PHONY: vendor

GO_PACKAGES=$(shell go list ./...)
GO ?= $(shell command -v go 2> /dev/null)

all: build

build: vendor check-style
	go build -mod=vendor

install: vendor check-style
	go install -mod=vendor

package: vendor check-style
	mkdir -p build

	@echo Build Linux amd64
	env GOOS=linux GOARCH=amd64 go build -mod=vendor
	tar cf build/linux_amd64.tar mmetl

	@echo Build OSX amd64
	env GOOS=darwin GOARCH=amd64 go build -mod=vendor
	tar cf build/darwin_amd64.tar mmetl

	@echo Build Windows amd64
	env GOOS=windows GOARCH=amd64 go build -mod=vendor
	zip build/windows_amd64.zip mmetl.exe

	rm mmetl mmetl.exe

golangci-lint:
# https://stackoverflow.com/a/677212/1027058 (check if a command exists or not)
	@if ! [ -x "$$(command -v golangci-lint)" ]; then \
		echo "golangci-lint is not installed. Please see https://github.com/golangci/golangci-lint#install for installation instructions."; \
		exit 1; \
	fi; \

	@echo Running golangci-lint
	golangci-lint run -E gofmt ./...

test:
	@echo Running tests
	$(GO) test -race -v $(GO_PACKAGES)

check-style: golangci-lint


verify-gomod:
	$(GO) mod download
	$(GO) mod verify


vendor:
	go mod vendor
