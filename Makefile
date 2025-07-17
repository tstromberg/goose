APP_NAME = github-pr-systray
VERSION = 1.0.0

.PHONY: build clean deps run

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
	CGO_ENABLED=1 GOOS=darwin GOARCH=amd64 go build -o dist/$(APP_NAME)-darwin-amd64 main.go
	CGO_ENABLED=1 GOOS=darwin GOARCH=arm64 go build -o dist/$(APP_NAME)-darwin-arm64 main.go

# Build for Linux
build-linux:
	CGO_ENABLED=1 GOOS=linux GOARCH=amd64 go build -o dist/$(APP_NAME)-linux-amd64 main.go
	CGO_ENABLED=1 GOOS=linux GOARCH=arm64 go build -o dist/$(APP_NAME)-linux-arm64 main.go

# Build for Windows
build-windows:
	CGO_ENABLED=1 GOOS=windows GOARCH=amd64 go build -o dist/$(APP_NAME)-windows-amd64.exe main.go
	CGO_ENABLED=1 GOOS=windows GOARCH=arm64 go build -o dist/$(APP_NAME)-windows-arm64.exe main.go

# Clean build artifacts
clean:
	rm -rf dist/
	rm -f $(APP_NAME)

# Create dist directory
dist:
	mkdir -p dist