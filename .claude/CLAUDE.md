# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

Ready to Review is a macOS/Linux/Windows menubar application that helps developers track GitHub pull requests. It shows a count of incoming/outgoing PRs and highlights when you're blocking someone's review. The app integrates with the Turn API to provide intelligent PR metadata about who's actually blocking progress.

## Key Commands

### Building and Running
```bash
make run          # Build and run (on macOS: installs to /Applications and launches)
make build        # Build for current platform
make app-bundle   # Create macOS .app bundle
make install      # Install to system (macOS: /Applications, Linux: /usr/local/bin, Windows: %LOCALAPPDATA%)
```

### Development
```bash
make lint         # Run all linters (golangci-lint with strict config + yamllint)
make fix          # Auto-fix linting issues where possible
make deps         # Download and tidy Go dependencies
make clean        # Remove build artifacts
```

## Architecture Overview

### Core Components

1. **Main Application Flow** (`main.go`)
   - Single `context.Background()` created in main, passed through all functions
   - App struct holds GitHub/Turn clients, PR data, and UI state
   - Update loop runs every 2 minutes to refresh PR data
   - Menu updates only rebuild when PR data actually changes (hash-based optimization)

2. **GitHub Integration** (`github.go`)
   - Uses GitHub CLI token (`gh auth token`) for authentication
   - Fetches PRs with a single optimized query: `is:open is:pr involves:USER archived:false`
   - No pagination needed (uses 100 per page limit)

3. **Turn API Integration** (`cache.go`)
   - Provides intelligent PR metadata (who's blocking, PR size, tags)
   - Implements 2-hour TTL cache with SHA256-based cache keys
   - Cache cleanup runs daily, removes files older than 5 days
   - Turn API calls are made for each PR to determine blocking status

4. **UI Management** (`ui.go`)
   - System tray integration via energye/systray
   - Menu structure: Incoming PRs → Outgoing PRs → Settings → About
   - Click handlers open PRs in browser with URL validation
   - "Hide stale PRs" option filters PRs older than 90 days

5. **Platform-Specific Features**
   - `loginitem_darwin.go`: macOS "Start at Login" functionality via AppleScript
   - `loginitem_other.go`: Stub for non-macOS platforms

### Key Design Decisions

- **No Context in Structs**: Context is always passed as a parameter, never stored
- **Graceful Degradation**: Turn API failures don't break the app, PRs still display
- **Security**: Only HTTPS URLs allowed, whitelist of github.com and dash.ready-to-review.dev
- **Minimal Dependencies**: Uses standard library where possible
- **Proper Cancellation**: All goroutines respect context cancellation

### Linting Configuration

The project uses an extremely strict golangci-lint configuration (`.golangci.yml`) that enforces:
- All available linters except those that conflict with Go best practices
- No nolint directives without explanations
- Cognitive complexity limit of 55
- No magic numbers (must use constants)
- Proper error handling (no unchecked errors)
- No naked returns except in very short functions
- Field alignment optimization for structs

### Special Considerations

1. **Authentication**: Uses GitHub CLI token, never stores it persistently
2. **Caching**: Turn API responses cached to reduce API calls
3. **Menu Updates**: Hash-based change detection prevents unnecessary UI updates
4. **Context Handling**: Single context from main, proper cancellation in all goroutines
5. **Error Handling**: All errors wrapped with context using `fmt.Errorf` with `%w`

When making changes:
- Run `make lint` and fix all issues without adding nolint directives
- Follow the strict Go style guidelines in ~/.claude/CLAUDE.md
- Keep functions simple and focused
- Test macOS-specific features carefully (login items, app bundle)