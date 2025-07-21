APP_NAME = ready-to-review
BUNDLE_NAME = Ready to Review
VERSION = 1.0.0
BUNDLE_ID = com.ready-to-review

.PHONY: build clean deps run app-bundle

# Install dependencies
deps:
	go mod download
	go mod tidy

# Run the application
run:
	go run main.go

# Build for current platform
build:
	CGO_ENABLED=1 go build -o $(APP_NAME) main.go

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
	CGO_ENABLED=1 GOOS=windows GOARCH=amd64 go build -o out/$(APP_NAME)-windows-amd64.exe main.go
	CGO_ENABLED=1 GOOS=windows GOARCH=arm64 go build -o out/$(APP_NAME)-windows-arm64.exe main.go

# Clean build artifacts
clean:
	rm -rf out/
	rm -f $(APP_NAME)

# Create out directory
out:
	mkdir -p out

# Build macOS application bundle
app-bundle: out build-darwin
	@echo "Creating macOS application bundle..."
	# Create app bundle structure
	mkdir -p "out/$(BUNDLE_NAME).app/Contents/MacOS"
	mkdir -p "out/$(BUNDLE_NAME).app/Contents/Resources"
	
	# Copy the binary
	cp out/$(APP_NAME)-darwin-$(shell uname -m) "out/$(BUNDLE_NAME).app/Contents/MacOS/$(APP_NAME)"
	
	# Generate icon if it doesn't exist
	@if [ ! -f out/AppIcon.icns ]; then \
		echo "Generating rocket emoji icon..."; \
		./scripts/generate_icon.sh && mv AppIcon.icns out/; \
	fi
	
	# Copy icon
	cp out/AppIcon.icns "out/$(BUNDLE_NAME).app/Contents/Resources/AppIcon.icns"
	
	# Create Info.plist
	@echo '<?xml version="1.0" encoding="UTF-8"?>' > "out/$(BUNDLE_NAME).app/Contents/Info.plist"
	@echo '<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">' >> "out/$(BUNDLE_NAME).app/Contents/Info.plist"
	@echo '<plist version="1.0">' >> "out/$(BUNDLE_NAME).app/Contents/Info.plist"
	@echo '<dict>' >> "out/$(BUNDLE_NAME).app/Contents/Info.plist"
	@echo '    <key>CFBundleExecutable</key>' >> "out/$(BUNDLE_NAME).app/Contents/Info.plist"
	@echo '    <string>$(APP_NAME)</string>' >> "out/$(BUNDLE_NAME).app/Contents/Info.plist"
	@echo '    <key>CFBundleIdentifier</key>' >> "out/$(BUNDLE_NAME).app/Contents/Info.plist"
	@echo '    <string>$(BUNDLE_ID)</string>' >> "out/$(BUNDLE_NAME).app/Contents/Info.plist"
	@echo '    <key>CFBundleName</key>' >> "out/$(BUNDLE_NAME).app/Contents/Info.plist"
	@echo '    <string>$(BUNDLE_NAME)</string>' >> "out/$(BUNDLE_NAME).app/Contents/Info.plist"
	@echo '    <key>CFBundleDisplayName</key>' >> "out/$(BUNDLE_NAME).app/Contents/Info.plist"
	@echo '    <string>$(BUNDLE_NAME)</string>' >> "out/$(BUNDLE_NAME).app/Contents/Info.plist"
	@echo '    <key>CFBundleVersion</key>' >> "out/$(BUNDLE_NAME).app/Contents/Info.plist"
	@echo '    <string>$(VERSION)</string>' >> "out/$(BUNDLE_NAME).app/Contents/Info.plist"
	@echo '    <key>CFBundleShortVersionString</key>' >> "out/$(BUNDLE_NAME).app/Contents/Info.plist"
	@echo '    <string>$(VERSION)</string>' >> "out/$(BUNDLE_NAME).app/Contents/Info.plist"
	@echo '    <key>CFBundleIconFile</key>' >> "out/$(BUNDLE_NAME).app/Contents/Info.plist"
	@echo '    <string>AppIcon</string>' >> "out/$(BUNDLE_NAME).app/Contents/Info.plist"
	@echo '    <key>LSUIElement</key>' >> "out/$(BUNDLE_NAME).app/Contents/Info.plist"
	@echo '    <true/>' >> "out/$(BUNDLE_NAME).app/Contents/Info.plist"
	@echo '    <key>NSHighResolutionCapable</key>' >> "out/$(BUNDLE_NAME).app/Contents/Info.plist"
	@echo '    <true/>' >> "out/$(BUNDLE_NAME).app/Contents/Info.plist"
	@echo '</dict>' >> "out/$(BUNDLE_NAME).app/Contents/Info.plist"
	@echo '</plist>' >> "out/$(BUNDLE_NAME).app/Contents/Info.plist"
	
	@echo "macOS app bundle created: out/$(BUNDLE_NAME).app"