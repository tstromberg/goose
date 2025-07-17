# pr-menubar

A cross-platform system tray application that monitors GitHub pull requests and shows how many are blocked on you.

## Features

- Shows PR count in system tray (format: "incoming/outgoing")
- Displays incoming PRs (PRs you need to review) and outgoing PRs (PRs you created)
- Highlights blocked PRs with red indicator
- Click on PRs to open them in your browser
- Integrates with Turn API for enhanced blocking detection
- Cross-platform support (macOS, Linux, Windows)

## Prerequisites

- Go 1.21 or later
- GitHub CLI (`gh`) installed and authenticated
- Access to GitHub repositories
- CGO enabled (required for system tray functionality)
- Platform-specific build tools for cross-compilation

## Installation

1. Clone this repository:
   ```bash
   git clone <repository-url>
   cd pr-menubar
   ```

2. Install dependencies:
   ```bash
   make deps
   ```

3. Ensure you're authenticated with GitHub CLI:
   ```bash
   gh auth login
   ```

## Usage

### Running the application

```bash
make run
```

### Building for current platform

```bash
make build
```

### Building for all platforms

```bash
make build-all
```

This will create binaries in the `dist/` directory for:
- macOS (Intel and Apple Silicon)
- Linux (x64 and ARM64)
- Windows (x64 and ARM64)

**Note**: Cross-compilation requires CGO and platform-specific toolchains. For Linux and Windows builds from macOS, you may need to install additional tools:

```bash
# For Linux cross-compilation
brew install FiloSottile/musl-cross/musl-cross

# For Windows cross-compilation  
brew install mingw-w64
```

## How it works

1. **Authentication**: Uses `gh auth token` to get your GitHub authentication token
2. **PR Discovery**: Searches for PRs using GitHub's search API with queries:
   - `is:open is:pr involves:{username} archived:false`
   - `is:open is:pr user:{username} archived:false`
3. **Turn API Integration**: Checks each PR against the Turn API to determine blocking status
4. **Categorization**:
   - **Incoming PRs**: PRs where you're not the author but are involved (reviewer, mentioned, etc.)
   - **Outgoing PRs**: PRs you created
5. **Blocking Detection**: Uses Turn API's NextAction data to determine if a PR is blocked on you

## System Tray Display

The system tray shows: `{incoming_blocked}/{outgoing_blocked}`

For example:
- `1/2` means 1 incoming PR blocked on you, 2 outgoing PRs blocked
- `0/0` means no PRs are currently blocked on you

## Menu Structure

When you click the system tray icon:

```
Incoming PRs (X blocked on you)
├── 🔴 repo/name #123 (if blocked on you)
├── repo/name #124
└── ...

Outgoing PRs (X blocked)
├── 🔴 repo/name #125 (if blocked)
├── repo/name #126
└── ...

Refresh
Quit
```

## Troubleshooting

- **No PRs showing**: Ensure you're authenticated with `gh auth status`
- **Turn API errors**: Check your network connection and GitHub token permissions
- **Build errors**: Ensure Go 1.21+ is installed and dependencies are up to date

## Development

The application refreshes PR data every 5 minutes automatically. You can also manually refresh using the "Refresh" menu item.

For development, you can modify the refresh interval in `main.go`:

```go
time.Sleep(5 * time.Minute) // Change this duration
```