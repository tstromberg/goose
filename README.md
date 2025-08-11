# Ready to Review 🎯

The smart PR tracker that knows when you're actually blocking someone. Lives in your menubar, tracks GitHub PRs, and plays sound effects when you need to pay attention.

![PR Menubar Screenshot](media/screenshot.png)

## Quick Start ⚡

```bash
# Install dependencies:
brew install gh go  # macOS (or visit https://cli.github.com)
gh auth login

# Install & run:
git clone https://github.com/ready-to-review/pr-menubar.git
cd pr-menubar && make run
```

The app appears in your menubar showing: 🪿 (incoming blocked on you) or 🎉 (outgoing blocked)

## Features

- **Smart Notifications**: Desktop alerts + sounds when PRs become blocked (🪿 honk for incoming, 🚀 rocket for outgoing)
- **Comprehensive Coverage**: Tracks PRs you're involved in + PRs in your repos needing reviewers
- **Detailed Tooltips**: Hover to see why you're blocking and what's needed
- **Test-Aware**: Waits for CI to pass before notifying
- **Zero Noise**: No pings for PRs that aren't actually blocked on you
- **One-Click Access**: Open any PR instantly from the menubar
- **Multi-User Support**: Track PRs for different GitHub accounts with `--user`
- **Auto-Start**: macOS "Start at Login" option (when running from /Applications)

## Installation

```bash
make run          # Quick install (macOS: installs to /Applications)
make install      # Traditional install for your OS
make build        # Build only
```

**Requirements**: GitHub CLI (`gh`) authenticated, Go 1.23+ (for building)

## Privacy

Your GitHub token (from `gh auth token`) is used to fetch PRs and authenticate with our API. We never store it.

---

Built with ❤️ by [CodeGroove](https://codegroove.dev/products/) • [Contribute](https://github.com/ready-to-review/pr-menubar)
