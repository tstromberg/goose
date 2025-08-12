# Ready-to-Review Goose 🪿

![Experimental](https://img.shields.io/badge/status-experimental-orange)
![Platform](https://img.shields.io/badge/platform-macOS%20%7C%20Linux%20%7C%20BSD%20%7C%20Windows-blue)
![Goose Noises](https://img.shields.io/badge/goose%20noises-100%25%20more-green)
[![GitHub](https://img.shields.io/github/stars/ready-to-review/goose?style=social)](https://github.com/ready-to-review/goose)

The only PR tracker that honks at you when you're the bottleneck. Now shipping with 100% more goose noises!

Lives in your menubar like a tiny waterfowl of productivity shame, watching your GitHub PRs and making aggressive bird sounds when you're blocking someone's code from seeing the light of production.

> ⚠️ **EXPERIMENTAL**: This is very much a work in progress. The blocking logic has bugs. Linux/BSD/Windows support is untested. Here be dragons (and geese).

![PR Menubar Screenshot](media/screenshot.png)

## What It Does

- **🪿 Honks** when you're blocking someone's PR (authentic goose noises included)
- **🎉 Tada sounds** when your own PR is ready for the next stage
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

For maximum security, use a [fine-grained personal access token](https://github.com/settings/personal-access-tokens/new):

1. Go to GitHub Settings → Developer settings → Personal access tokens → Fine-grained tokens
2. Create a new token with:
   - **Expiration**: Set a short expiration (30-90 days recommended)
   - **Repository access**: Select only the specific repositories you want to monitor
   - **Permissions**:
     - Pull requests: Read
     - Metadata: Read
3. Copy the token (starts with `github_pat_`)

If you need broader access, you can use a [classic token](https://github.com/settings/tokens):
- Create with `repo` scope (grants full repository access - use with caution)

#### Using the Token

```bash
export GITHUB_TOKEN=your_token_here
git clone https://github.com/ready-to-review/goose.git
cd goose && make run
```

When `GITHUB_TOKEN` is set, the goose will use it directly instead of the GitHub CLI, giving you precise control over repository access. Fine-grained tokens are strongly recommended for better security.

## Known Issues

- Blocking logic isn't 100% accurate (we're working on it)
- The goose may not stop honking until you review your PRs
- Visual notifications won't work on macOS until we sign the binary

## Pricing

This tool is part of the [CodeGroove](https://codegroove.dev) developer acceleration platform:
- **Forever free** for open-source repositories
- Low-cost fee TBD for access to private repos (the goose needs to eat)

## Privacy

- Your GitHub token is never stored or logged
- PR metadata cached for up to 20 days (performance)
- No telemetry or external data collection

---

Built with ❤️ by [CodeGroove](https://codegroove.dev/products/)

[Contribute](https://github.com/ready-to-review/goose) (PRs welcome, but the goose will judge you)
