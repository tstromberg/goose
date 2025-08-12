# Ready-to-Review Goose 🪿

![Experimental](https://img.shields.io/badge/status-experimental-orange)
![Platform](https://img.shields.io/badge/platform-macOS%20%7C%20Linux%20%7C%20BSD%20%7C%20Windows-blue)
![Goose Noises](https://img.shields.io/badge/goose%20noises-100%25%20more-green)
[![GitHub](https://img.shields.io/github/stars/ready-to-review/goose?style=social)](https://github.com/ready-to-review/goose)

The only PR tracker that honks at you when you're the bottleneck. Now shipping with 100% more goose noises!

Lives in your menubar like a tiny waterfowl of productivity shame, watching your GitHub PRs and making aggressive bird sounds when you're blocking someone's code from seeing the light of production.

> ⚠️ **EXPERIMENTAL**: This is very much a work in progress. The blocking logic has bugs. It theoretically runs on Linux, BSD, and Windows but we've literally never tested it there. Here be dragons (and geese).

![PR Menubar Screenshot](media/screenshot.png)

## What It Does

- **🪿 Honks** when you're blocking someone's PR (authentic goose noises included)
- **🚀 Rocket sounds** when own your own PR is ready to go to the next stage
- **🧠 Smart turn-based assignment** - knows who is blocking a PR, knows when tests are failing, etc.
- **⭐ Auto-start** on login (macOS)

You can also visit the web-based equivalent at https://dash.ready-to-review.dev/

## macOS Quick Start ⚡ (How to Get Honked At)

### Option 1: Using GitHub CLI (Default)

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

### Option 2: Using a GitHub Token (More Control)

If you want more control over which repositories the goose can access, you can use a GitHub personal access token instead:

1. Create a [GitHub personal access token](https://github.com/settings/tokens) with `repo` scope
2. Set the `GITHUB_TOKEN` environment variable:

```bash
export GITHUB_TOKEN=your_token_here
git clone https://github.com/ready-to-review/goose.git
cd goose && make run
```

When `GITHUB_TOKEN` is set, the goose will use it directly instead of the GitHub CLI, giving you precise control over repository access.

## Known Issues

- Blocking logic isn't 100% accurate (we're working on it)
- Linux/BSD/Windows support likely works, but remains untested
- The goose may not stop honking until you review your PRs
- Visual notifications won't work on macOS until we sign the binary

## Pricing

This tool is part of the [CodeGroove](https://codegroove.dev) developer acceleration platform:
- **Forever free** for open-source repositories
- Low-cost fee TBD for access to private repos (the goose needs to eat)

## Privacy

- Your GitHub token is used to fetch PR metadata, but is never stored or logged.
- We won't sell your information or use it for any purpose other than caching.
- GitHub metadata for open pull requests may be cached for up to 20 days for performance reasons.

---

Built with ❤️ by [CodeGroove](https://codegroove.dev/products/)

[Contribute](https://github.com/ready-to-review/goose) (PRs welcome, but the goose will judge you)
