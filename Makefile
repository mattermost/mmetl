GO_PACKAGES=$(shell go list ./...)
GO ?= $(shell command -v go 2> /dev/null)
BUILD_HASH ?= $(shell git rev-parse HEAD)
BUILD_VERSION ?= $(shell git ls-remote --tags --refs https://github.com/mattermost/mmetl.git | tail -n1 | sed 's/.*\///')

LDFLAGS += -X "github.com/mattermost/mmetl/commands.BuildHash=$(BUILD_HASH)"
LDFLAGS += -X "github.com/mattermost/mmetl/commands.Version=$(BUILD_VERSION)"
BUILD_COMMAND ?= go build -ldflags '$(LDFLAGS)'
all: deploy-local

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

golangci-lint:
# https://stackoverflow.com/a/677212/1027058 (check if a command exists or not)
	@if ! [ -x "$$(command -v golangci-lint)" ]; then \
		echo "golangci-lint is not installed. Please see https://github.com/golangci/golangci-lint#install for installation instructions."; \
		exit 1; \
	fi; \

	@echo Running golangci-lint
	golangci-lint run --skip-dirs-use-default --timeout 5m -E gofmt ./...


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
	$(GO) test -race -v $(GO_PACKAGES)

check-style: golangci-lint


verify-gomod:
	$(GO) mod download
	$(GO) mod verify

tidy:
	go mod tidy

deploy-local: package
	# move to parent directories for mac and linux
	# untar to bin/mac and bin/linux
	mkdir -p ../bin/mac ../bin/linux
	tar -xzf build/darwin_amd64.tar.gz -C ../bin/mac
	tar -xzf build/linux_amd64.tar.gz -C ../bin/linux
