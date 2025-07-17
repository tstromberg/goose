# PR Menubar: Your friendly neighborhood PR reminder!

![PR Menubar Screenshot](media/screenshot.png)

NOTE: This is an early experiment to support what we are working on at https://codegroove.dev/products/

Tired of being the bottleneck? `pr-menubar` lives in your system tray and shows you how many GitHub pull requests are waiting for *you*.

It's a simple way to stay on top of your reviews and keep your team moving.

## Quick Start

1.  **Prerequisites**: Make sure you have Go 1.21+ and the GitHub CLI (`gh`) installed and authenticated.
2.  **Clone & Run**:
    ```bash
    git clone https://github.com/turn-systems/pr-menubar.git
    cd pr-menubar
    go run .
    ```

That's it! The app will appear in your system tray.

## How to Read It

The tray icon shows `incoming / outgoing` PRs that are currently blocked.

-   ` incoming`: PRs by others that need your review.
-   ` outgoing`: Your own PRs that are blocked by someone else.

A `❗` next to a PR in the menu means it's blocked. Click any PR to open it directly in your browser.

Happy reviewing! ✨
