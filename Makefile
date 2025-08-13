APP_NAME = ready-to-review
BUNDLE_NAME = Ready to Review
VERSION = 1.0.0
BUNDLE_VERSION = 1
BUNDLE_ID = dev.codegroove.r2r

# Version information for builds
GIT_VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
GIT_COMMIT := $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILD_DATE := $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")
LDFLAGS := -X main.version=$(GIT_VERSION) -X main.commit=$(GIT_COMMIT) -X main.date=$(BUILD_DATE)

.PHONY: all build clean deps run app-bundle install install-darwin install-unix install-windows

# Default target
all: build

# Install dependencies
deps:
	go mod download
	go mod tidy

# Run the application
run:
ifeq ($(shell uname),Darwin)
	@$(MAKE) install
	@echo "Running $(BUNDLE_NAME) from /Applications..."
	@open "/Applications/$(BUNDLE_NAME).app"
else
	go run ./cmd/goose
endif

# Build for current platform
build: out
ifeq ($(OS),Windows_NT)
	CGO_ENABLED=1 go build -ldflags "-H=windowsgui $(LDFLAGS)" -o out/$(APP_NAME).exe ./cmd/goose
else
	CGO_ENABLED=1 go build -ldflags "$(LDFLAGS)" -o out/$(APP_NAME) ./cmd/goose
endif

# Build for all platforms
build-all: build-darwin build-linux build-windows

# Build for macOS
build-darwin:
	CGO_ENABLED=1 GOOS=darwin GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o out/$(APP_NAME)-darwin-amd64 ./cmd/goose
	CGO_ENABLED=1 GOOS=darwin GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o out/$(APP_NAME)-darwin-arm64 ./cmd/goose

# Build for Linux
build-linux:
	CGO_ENABLED=1 GOOS=linux GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o out/$(APP_NAME)-linux-amd64 ./cmd/goose
	CGO_ENABLED=1 GOOS=linux GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o out/$(APP_NAME)-linux-arm64 ./cmd/goose

# Build for Windows
build-windows:
	CGO_ENABLED=1 GOOS=windows GOARCH=amd64 go build -ldflags "-H=windowsgui $(LDFLAGS)" -o out/$(APP_NAME)-windows-amd64.exe ./cmd/goose
	CGO_ENABLED=1 GOOS=windows GOARCH=arm64 go build -ldflags "-H=windowsgui $(LDFLAGS)" -o out/$(APP_NAME)-windows-arm64.exe .

# Clean build artifacts
clean:
	rm -rf out/

# Create out directory
out:
	mkdir -p out

# Install appify if not already installed
install-appify:
	@if ! command -v appify &> /dev/null; then \
		echo "Installing appify..."; \
		go install github.com/machinebox/appify@latest; \
	else \
		echo "appify is already installed"; \
	fi

# Build macOS application bundle using appify
app-bundle: out build-darwin install-appify
	@echo "Removing old app bundle..."
	@rm -rf "out/$(BUNDLE_NAME).app"

	@echo "Creating macOS application bundle with appify..."

	# Create universal binary
	@echo "Creating universal binary..."
	lipo -create out/$(APP_NAME)-darwin-amd64 out/$(APP_NAME)-darwin-arm64 \
		-output out/$(APP_NAME)-universal

	# Copy logo to out directory
	cp media/logo.png out/logo.png

	# Create menubar icon (small version with transparency)
	@echo "Creating menubar icon..."
	sips -z 44 44 media/logo.png --out out/menubar-icon.png
	# Ensure the icon has an alpha channel
	sips -s format png out/menubar-icon.png --out out/menubar-icon.png

	# Create app bundle with appify using universal binary
	cd out && appify -name "$(BUNDLE_NAME)" \
		-icon logo.png \
		-id "$(BUNDLE_ID)" \
		$(APP_NAME)-universal

	# Move the generated app to the expected location
	@if [ -f "out/$(BUNDLE_NAME)-universal.app" ]; then \
		mv "out/$(BUNDLE_NAME)-universal.app" "out/$(BUNDLE_NAME).app"; \
	elif [ ! -d "out/$(BUNDLE_NAME).app" ]; then \
		echo "Warning: App bundle not found in expected location"; \
	fi

	# Copy menubar icon to Resources
	@echo "Copying menubar icon to app bundle..."
	cp out/menubar-icon.png "out/$(BUNDLE_NAME).app/Contents/Resources/menubar-icon.png"

	# Create English localization
	@echo "Creating English localization..."
	mkdir -p "out/$(BUNDLE_NAME).app/Contents/Resources/en.lproj"

	# Fix the executable name (appify adds .app suffix which we don't want)
	@echo "Fixing executable name..."
	@if [ -f "out/$(BUNDLE_NAME).app/Contents/MacOS/$(BUNDLE_NAME).app" ]; then \
		mv "out/$(BUNDLE_NAME).app/Contents/MacOS/$(BUNDLE_NAME).app" "out/$(BUNDLE_NAME).app/Contents/MacOS/$(BUNDLE_NAME)"; \
	fi

	# Fix the Info.plist
	@echo "Fixing Info.plist..."
	@/usr/libexec/PlistBuddy -c "Set :CFBundleExecutable Ready\\ to\\ Review" "out/$(BUNDLE_NAME).app/Contents/Info.plist"
	@/usr/libexec/PlistBuddy -c "Add :LSUIElement bool true" "out/$(BUNDLE_NAME).app/Contents/Info.plist" 2>/dev/null || \
		/usr/libexec/PlistBuddy -c "Set :LSUIElement true" "out/$(BUNDLE_NAME).app/Contents/Info.plist"
	@/usr/libexec/PlistBuddy -c "Add :CFBundleDevelopmentRegion string en" "out/$(BUNDLE_NAME).app/Contents/Info.plist" 2>/dev/null || \
		/usr/libexec/PlistBuddy -c "Set :CFBundleDevelopmentRegion en" "out/$(BUNDLE_NAME).app/Contents/Info.plist"
	@/usr/libexec/PlistBuddy -c "Add :NSUserNotificationAlertStyle string alert" "out/$(BUNDLE_NAME).app/Contents/Info.plist" 2>/dev/null || \
		/usr/libexec/PlistBuddy -c "Set :NSUserNotificationAlertStyle alert" "out/$(BUNDLE_NAME).app/Contents/Info.plist"

	# Remove extended attributes and code sign the app bundle
	@echo "Preparing app bundle for signing..."
	xattr -cr "out/$(BUNDLE_NAME).app"

	@echo "Code signing the app bundle..."
	codesign --force --deep --sign - --options runtime "out/$(BUNDLE_NAME).app"

	@echo "macOS app bundle created: out/$(BUNDLE_NAME).app"

# Install the application (detects OS automatically)
install:
ifeq ($(shell uname),Darwin)
	@$(MAKE) install-darwin
else ifeq ($(OS),Windows_NT)
	@$(MAKE) install-windows
else ifneq ($(filter $(shell uname),Linux FreeBSD OpenBSD NetBSD SunOS),)
	@$(MAKE) install-unix
else
	@echo "Unsupported platform. Please install manually."
	@exit 1
endif

# Install on macOS
install-darwin: app-bundle
	@echo "Installing on macOS..."
	@echo "Copying $(BUNDLE_NAME).app to /Applications..."
	@rm -rf "/Applications/$(BUNDLE_NAME).app"
	@cp -R "out/$(BUNDLE_NAME).app" "/Applications/"
	@echo "Installation complete! $(BUNDLE_NAME) has been installed to /Applications"

# Install on Unix-like systems (Linux, BSD variants, Solaris)
install-unix: build
	@echo "Installing on $(shell uname)..."
	@echo "Installing binary to /usr/local/bin..."
	@sudo install -m 755 out/$(APP_NAME) /usr/local/bin/
	@echo "Installation complete! $(APP_NAME) has been installed to /usr/local/bin"

# Install on Windows
install-windows: build
	@echo "Installing on Windows..."
	@echo "Creating program directory..."
	@if not exist "%LOCALAPPDATA%\Programs\$(APP_NAME)" mkdir "%LOCALAPPDATA%\Programs\$(APP_NAME)"
	@echo "Copying executable..."
	@copy /Y "out\$(APP_NAME).exe" "%LOCALAPPDATA%\Programs\$(APP_NAME)\"
	@echo "Installation complete! $(APP_NAME) has been installed to %LOCALAPPDATA%\Programs\$(APP_NAME)"
	@echo "You may want to add %LOCALAPPDATA%\Programs\$(APP_NAME) to your PATH environment variable."
# BEGIN: lint-install .
# http://github.com/codeGROOVE-dev/lint-install

.PHONY: lint
lint: _lint

LINT_ARCH := $(shell uname -m)
LINT_OS := $(shell uname)
LINT_OS_LOWER := $(shell echo $(LINT_OS) | tr '[:upper:]' '[:lower:]')
LINT_ROOT := $(shell dirname $(realpath $(firstword $(MAKEFILE_LIST))))

# shellcheck and hadolint lack arm64 native binaries: rely on x86-64 emulation
ifeq ($(LINT_OS),Darwin)
	ifeq ($(LINT_ARCH),arm64)
		LINT_ARCH=x86_64
	endif
endif

LINTERS :=
FIXERS :=

GOLANGCI_LINT_CONFIG := $(LINT_ROOT)/.golangci.yml
GOLANGCI_LINT_VERSION ?= v2.3.1
GOLANGCI_LINT_BIN := $(LINT_ROOT)/out/linters/golangci-lint-$(GOLANGCI_LINT_VERSION)-$(LINT_ARCH)
$(GOLANGCI_LINT_BIN):
	mkdir -p $(LINT_ROOT)/out/linters
	rm -rf $(LINT_ROOT)/out/linters/golangci-lint-*
	curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh | sh -s -- -b $(LINT_ROOT)/out/linters $(GOLANGCI_LINT_VERSION)
	mv $(LINT_ROOT)/out/linters/golangci-lint $@

LINTERS += golangci-lint-lint
golangci-lint-lint: $(GOLANGCI_LINT_BIN)
	find . -name go.mod -execdir "$(GOLANGCI_LINT_BIN)" run -c "$(GOLANGCI_LINT_CONFIG)" \;

FIXERS += golangci-lint-fix
golangci-lint-fix: $(GOLANGCI_LINT_BIN)
	find . -name go.mod -execdir "$(GOLANGCI_LINT_BIN)" run -c "$(GOLANGCI_LINT_CONFIG)" --fix \;

YAMLLINT_VERSION ?= 1.37.1
YAMLLINT_ROOT := $(LINT_ROOT)/out/linters/yamllint-$(YAMLLINT_VERSION)
YAMLLINT_BIN := $(YAMLLINT_ROOT)/dist/bin/yamllint
$(YAMLLINT_BIN):
	mkdir -p $(LINT_ROOT)/out/linters
	rm -rf $(LINT_ROOT)/out/linters/yamllint-*
	curl -sSfL https://github.com/adrienverge/yamllint/archive/refs/tags/v$(YAMLLINT_VERSION).tar.gz | tar -C $(LINT_ROOT)/out/linters -zxf -
	cd $(YAMLLINT_ROOT) && pip3 install --target dist . || pip install --target dist .

LINTERS += yamllint-lint
yamllint-lint: $(YAMLLINT_BIN)
	PYTHONPATH=$(YAMLLINT_ROOT)/dist $(YAMLLINT_ROOT)/dist/bin/yamllint .

.PHONY: _lint $(LINTERS)
_lint: $(LINTERS)

.PHONY: fix $(FIXERS)
fix: $(FIXERS)

# END: lint-install .
