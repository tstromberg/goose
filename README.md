# pr-menubar

A system tray app that monitors GitHub pull requests and shows how many are blocked on you.

## Features

- Shows PR count in system tray (format: "incoming/outgoing")
- Displays incoming PRs (PRs you need to review) and outgoing PRs (PRs you created)
- Highlights blocked PRs with red indicator
- Click on PRs to open them in your browser
- Integrates with Turn API for enhanced blocking detection
- Desktop notifications when PRs become blocked (experimental)

## Prerequisites

- Go 1.21+
- GitHub CLI (`gh`) installed and authenticated
- CGO enabled

## Installation

1. Clone this repository:
   ```bash
   git clone <repository-url>
   cd pr-menubar
   ```

2. Install dependencies:
   ```bash
   go mod download
   ```

3. Ensure you're authenticated with GitHub CLI:
   ```bash
   gh auth login
   ```

## Usage

Run the application:

```bash
go run .
```

Build for current platform:

```bash
go build -o pr-menubar
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

The application refreshes PR data every 2 minutes automatically. You can also manually refresh using the "Refresh" menu item.