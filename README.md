# Ready to Review 🎯

**Stop being the blocker. Get notified only when it matters.**

The smartest PR tracker that knows when you're actually blocking someone - not just when you're assigned. Only alerts when tests pass and your review is truly needed.

![PR Menubar Screenshot](media/screenshot.png)

📊 **Live PR counter** • 🎯 **Only notifies when YOU'RE the blocker** • ✅ **Waits for tests to pass** • ⚡ **One-click access**

## The Problem We Solve 🎯

- **"Did I forget to review that PR?"** - Never again. See pending reviews at a glance.
- **"Is my PR blocking the team?"** - Instant visibility on blocked PRs with ❗ indicators.
- **"Which PR should I review first?"** - Smart prioritization shows you what matters most.
- **"I hate context switching to check PRs"** - Stay in your flow. Check without leaving your work.

## Quick Start ⚡

```bash
# Install dependencies if you don't have them:
brew install gh go  # macOS (or visit https://cli.github.com and https://go.dev)
gh auth login

# Install & run Ready to Review:
git clone https://github.com/turn-systems/pr-menubar.git && cd pr-menubar && make run
```

That's it! The app appears in your menubar showing your PR count. Click to see all PRs with smart prioritization.

**Perfect for:**
- ✅ Teams doing 10+ PRs/week
- ✅ Open-source contributors
- ✅ Remote/async teams across timezones
- ✅ Anyone who's ever felt guilty about blocking a PR

## Why Not Just GitHub Notifications? 🤔

GitHub notifications are noisy and overwhelming. Ready to Review is different:

- **Actually Smart**: Only notifies when YOU are the specific blocker - not just because you're assigned
- **Test-Aware**: Waits for tests to pass before alerting you - no more reviewing broken PRs
- **Context-Aware**: Knows when someone explicitly asked for your help vs. automatic assignment
- **Zero Noise**: No pings for PRs that aren't actually blocked on you

## How It Works ✨

Ready to Review displays a simple counter in your menubar: `incoming / outgoing`

- **Incoming** 📥: PRs from teammates waiting for your review
- **Outgoing** 📤: Your PRs waiting on others

Blocked PRs are marked with ❗. Click any PR to open it in your browser.

### Auto-Start (macOS) 🌟

Click the menubar icon and toggle "Start at Login". Never think about it again!

## Authentication & Privacy 🔐

Ready to Review uses your GitHub CLI token (`gh auth token`) to:
- Fetch your PRs from GitHub
- Authenticate with our API server which intelligently determines when you're actually blocking a PR

**Your token never gets stored on our servers** - we use it to make GitHub API requests, then forget about it.

## Installation Options

**Quick Install** (recommended):
```bash
make run  # On macOS: installs to /Applications and launches
```

**Traditional Install**:
```bash
make install  # Installs to the right place for your OS:
             # macOS: /Applications/Ready to Review.app
             # Linux/BSD: /usr/local/bin/ready-to-review
             # Windows: %LOCALAPPDATA%\Programs\ready-to-review
```

**Build Only**:
```bash
make build        # Build for current platform
make app-bundle   # macOS: create .app bundle
```

**Requirements**:
- GitHub CLI (`gh`) installed and authenticated
- Go 1.21+ (for building from source)

## Contributing 🤝

Open-source contributions are welcome! Got an idea? Send a PR and we'll ship it. It's that simple.

---

### 🌟 Make Your Team Happier Today

No more blocked PRs. No more forgotten reviews. Just smooth, efficient collaboration.

Built with ❤️ by [CodeGroove](https://codegroove.dev/products/) for teams who ship fast.
