# Ready to Review

![PR Menubar Screenshot](media/screenshot.png)

A menubar app that keeps you on top of GitHub pull requests. Never be the bottleneck again!

## What It Does

Ready to Review lives in your system tray and shows a real-time count of pull requests that need your attention. The format is simple: `incoming / outgoing`

- **Incoming**: PRs from others waiting for your review
- **Outgoing**: Your PRs blocked by others

Click the icon to see all PRs, with blocked ones marked with `❗`. Click any PR to open it in your browser.

## Installation

### Prerequisites
- Go 1.21+
- GitHub CLI (`gh`) installed and authenticated

### Quick Install

```bash
git clone https://github.com/turn-systems/pr-menubar.git
cd pr-menubar
make install
```

This will:
- **macOS**: Build and install the app to `/Applications`
- **Linux/BSD/Solaris**: Build and install the binary to `/usr/local/bin`
- **Windows**: Build and install to `%LOCALAPPDATA%\Programs\ready-to-review`

### Manual Build

```bash
# Build for current platform
make build

# macOS: Create app bundle
make app-bundle

# Run directly without installing
go run .
```

## Development

This project is part of our work at [CodeGroove](https://codegroove.dev/products/).

Happy reviewing! ✨
