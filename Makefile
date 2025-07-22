APP_NAME = ready-to-review
BUNDLE_NAME = Ready to Review
VERSION = 1.0.0
BUNDLE_VERSION = 1
BUNDLE_ID = dev.codegroove.r2r

.PHONY: build clean deps run app-bundle install install-darwin install-unix install-windows

# Install dependencies
deps:
	go mod download
	go mod tidy

# Run the application
run:
	go run main.go

# Build for current platform
build:
ifeq ($(OS),Windows_NT)
	CGO_ENABLED=1 go build -ldflags -H=windowsgui -o $(APP_NAME).exe main.go
else
	CGO_ENABLED=1 go build -o $(APP_NAME) main.go
endif

# Build for all platforms
build-all: build-darwin build-linux build-windows

# Build for macOS
build-darwin:
	CGO_ENABLED=1 GOOS=darwin GOARCH=amd64 go build -o out/$(APP_NAME)-darwin-amd64 main.go
	CGO_ENABLED=1 GOOS=darwin GOARCH=arm64 go build -o out/$(APP_NAME)-darwin-arm64 main.go

# Build for Linux
build-linux:
	CGO_ENABLED=1 GOOS=linux GOARCH=amd64 go build -o out/$(APP_NAME)-linux-amd64 main.go
	CGO_ENABLED=1 GOOS=linux GOARCH=arm64 go build -o out/$(APP_NAME)-linux-arm64 main.go

# Build for Windows
build-windows:
	CGO_ENABLED=1 GOOS=windows GOARCH=amd64 go build -ldflags -H=windowsgui -o out/$(APP_NAME)-windows-amd64.exe main.go
	CGO_ENABLED=1 GOOS=windows GOARCH=arm64 go build -ldflags -H=windowsgui -o out/$(APP_NAME)-windows-arm64.exe main.go

# Clean build artifacts
clean:
	rm -rf out/
	rm -f $(APP_NAME)

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
	@sudo install -m 755 $(APP_NAME) /usr/local/bin/
	@echo "Installation complete! $(APP_NAME) has been installed to /usr/local/bin"

# Install on Windows
install-windows: build
	@echo "Installing on Windows..."
	@echo "Creating program directory..."
	@if not exist "%LOCALAPPDATA%\Programs\$(APP_NAME)" mkdir "%LOCALAPPDATA%\Programs\$(APP_NAME)"
	@echo "Copying executable..."
	@copy /Y "$(APP_NAME).exe" "%LOCALAPPDATA%\Programs\$(APP_NAME)\"
	@echo "Installation complete! $(APP_NAME) has been installed to %LOCALAPPDATA%\Programs\$(APP_NAME)"
	@echo "You may want to add %LOCALAPPDATA%\Programs\$(APP_NAME) to your PATH environment variable."