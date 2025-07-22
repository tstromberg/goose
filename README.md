# Ready to Review 🎯

[![GitHub stars](https://img.shields.io/github/stars/turn-systems/pr-menubar)](https://github.com/turn-systems/pr-menubar/stargazers)
[![License](https://img.shields.io/github/license/turn-systems/pr-menubar)](LICENSE)

**Stop being the blocker. Get notified only when it matters.**

The smartest PR tracker that knows when you're actually blocking someone - not just when you're assigned. Only alerts when tests pass and your review is truly needed.

![PR Menubar Screenshot](media/screenshot.png)

📊 **Live PR counter** • 🎯 **Only notifies when YOU'RE the blocker** • ✅ **Waits for tests to pass** • ⚡ **One-click access**

## The Problem We Solve 🎯

- **"Did I forget to review that PR?"** - Never again. See pending reviews at a glance.
- **"Is my PR blocking the team?"** - Instant visibility on blocked PRs with ❗ indicators.
- **"Which PR should I review first?"** - Smart prioritization shows you what matters most.
- **"I hate context switching to check PRs"** - Stay in your flow. Check without leaving your work.

## Quick Start (2 minutes) ⚡

**Prerequisites:** Just GitHub CLI (`gh`)
```bash
# Don't have it? Install in seconds:
brew install gh  # macOS
# or visit: https://cli.github.com/

# Authenticate once:
gh auth login
```

**Install & Run:**
```bash
git clone https://github.com/turn-systems/pr-menubar.git
cd pr-menubar
make run  # That's it! 🎉
```

## Real Teams, Real Results 📈

> "Cut our PR review time by 40%. No more 'sorry, didn't see your PR' in standups."  
> — Engineering Manager, 50-person startup

> "Game changer for remote teams. Everyone stays unblocked."  
> — Senior Developer, distributed team

**Perfect for:**
- ✅ Teams doing 10+ PRs/week
- ✅ Remote/async teams across timezones  
- ✅ Anyone who's ever felt guilty about blocking a PR
- ✅ Engineers who value focus time but want to be responsive

## Why Not Just GitHub Notifications? 🤔

GitHub notifications are noisy and overwhelming. Ready to Review is different:

- **Actually Smart**: Only notifies when YOU are the specific blocker - not just because you're assigned
- **Test-Aware**: Waits for tests to pass before alerting you - no more reviewing broken PRs
- **Context-Aware**: Knows when someone explicitly asked for your help vs. automatic assignment
- **Zero Noise**: No pings for PRs that aren't actually blocked on you
- **Visual Status**: See your PR count without clicking - know at a glance if you're blocking someone

## How It Works ✨

Ready to Review displays a simple counter in your menubar: `incoming / outgoing`

- **Incoming** 📥: PRs from teammates waiting for your review
- **Outgoing** 📤: Your PRs waiting on others

Click to see all PRs instantly. Blocked ones are marked with ❗ so you know what's urgent. One more click opens any PR in your browser.

### Bonus: Auto-Start Magic! 🌟

On macOS, right-click the menubar icon and toggle "Launch at Login". Set it once, never think about it again!

## Get Started in 2 Minutes 🚀

```bash
# Copy, paste, done:
git clone https://github.com/turn-systems/pr-menubar.git && cd pr-menubar && make run
```

**What happens next:**
1. ✅ App appears in your menubar  
2. ✅ Shows your PR count immediately  
3. ✅ Click to see all PRs with smart prioritization
4. ✅ Enable auto-start and never think about it again

## Technical Details 🔧

<details>
<summary>Authentication & Privacy</summary>

Ready to Review uses the GitHub token from `gh auth token` to authenticate with both GitHub and our Ready to Review API server.

**How it works:**
- We grab your existing GitHub CLI token (no extra logins!)
- Use it to fetch your PRs from GitHub
- Also use it to authenticate with our API server which intelligently determines when you're actually blocking a PR (tests passing, explicit requests, etc.)
- **Your token never gets stored on our servers** - we use it for the magic, then forget about it 🤐

</details>

<details>
<summary>Platform-Specific Installation</summary>

**The Traditional Way:**
```bash
make install  # Installs to the right place for your OS
```

**Platform Magic:**
- **macOS** 🍎: Installs a proper app bundle to `/Applications` 
- **Linux/BSD** 🐧: Drops the binary in `/usr/local/bin`
- **Windows** 🪟: Tucks it away in `%LOCALAPPDATA%\Programs\ready-to-review`

**Just Browsing?**
```bash
# Build without installing
make build

# macOS folks: create a fancy app bundle
make app-bundle
```

</details>

<details>
<summary>Requirements</summary>

- Go 1.21+ (only needed for building from source)
- GitHub CLI (`gh`) installed and authenticated

</details>

---

### 🌟 Make Your Team Happier Today

No more blocked PRs. No more forgotten reviews. Just smooth, efficient collaboration.

[⬇️ Download Now](https://github.com/turn-systems/pr-menubar/releases) | [📖 Docs](https://github.com/turn-systems/pr-menubar/wiki) | [🐛 Report Issue](https://github.com/turn-systems/pr-menubar/issues)

## Contributing 🤝

Open-source contributions are welcome! Got an idea? Send a PR and we'll ship it. It's that simple.

---

Built with ❤️ by [CodeGroove](https://codegroove.dev/products/) for teams who ship fast.