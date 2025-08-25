# Review Goose 🪿

![Beta](https://img.shields.io/badge/status-beta-orange)
![Platform](https://img.shields.io/badge/platform-macOS%20%7C%20Linux%20%7C%20BSD%20%7C%20Windows-blue)
![Goose Noises](https://img.shields.io/badge/goose%20noises-100%25%20more-green)
[![GitHub](https://img.shields.io/github/stars/ready-to-review/goose?style=social)](https://github.com/ready-to-review/goose)

![Review Goose Logo](media/logo-small.png)

The only PR tracker that honks at you when you're the bottleneck. Now shipping with 100% more goose noises!

Lives in your menubar like a tiny waterfowl of productivity shame, watching your GitHub PRs and making aggressive bird sounds when you're blocking someone's code from seeing the light of production.

![Review Goose Screenshot](media/screenshot.png)

## What It Does

- **🪿 Honks** when you're blocking someone's PR (authentic goose noises included)
- **✈️ Jet sounds** when your own PR is ready for the next stage
- **🧠 Smart turn-based assignment** - knows who is blocking a PR, knows when tests are failing, etc.
- **⭐ Auto-start** on login (macOS)
- **🔔 Auto-open** incoming PRs in your browser (off by default, rate-limited)

You can also visit the web-based equivalent at https://dash.ready-to-review.dev/

## macOS Quick Start ⚡ (How to Get Honked At)

### Option 1: Authenticating using GitHub CLI

Install dependencies: the [GitHub CLI, aka "gh"](https://cli.github.com/) and [Go](https://go.dev/):

```bash
brew install gh go
gh auth login
```

Then summon the goose:

```bash
git clone https://github.com/ready-to-review/goose.git
cd goose && make run
```

### Option 2: Using a fine-grained access token

If you want more control over which repositories the goose can access, you can use a [fine-grained personal access token](https://github.com/settings/personal-access-tokens/new) with the following permissions:

- **Pull requests**: Read
- **Metadata**: Read

You can then use the token like so:

```bash
export GITHUB_TOKEN=your_token_here
git clone https://github.com/ready-to-review/goose.git
cd goose && make run
```

We don't yet try to persist fine-grained tokens to disk - PR's welcome!

## Known Issues

- Blocking logic isn't 100% accurate - issues welcome!
- The goose may not stop honking until you review your PRs
- Visual notifications won't work on macOS until we sign the binary
- Linux, BSD, and Windows support is implemented but untested

## Pricing

The Goose is part of the [codeGROOVE](https://codegroove.dev) developer acceleration platform:
- **FREE forever** for open-source or public repositories
- GitHub Sponsors gain access to private repos ($2.56/mo recommended)

## Privacy

- Your GitHub token is used to authenticate against GitHub and codeGROOVE's API for state-machine & natural-language processing
- Your GitHub token is never stored or logged,
- PR metadata may be locally or remotely cached for up to 20 days (performance)
- No telemetry is collected

---

Built with ❤️ by [codeGROOVE](https://codegroove.dev/) - PRs welcome!
