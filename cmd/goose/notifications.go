// Package main - notifications.go provides simplified notification handling.
package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/gen2brain/beeep"
)

// processNotifications handles notifications for newly blocked PRs using the state manager.
func (app *App) processNotifications(ctx context.Context) {
	log.Print("[NOTIFY] Processing notifications...")

	// Get the list of PRs that need notifications
	app.mu.RLock()
	hiddenOrgs := make(map[string]bool)
	for org, hidden := range app.hiddenOrgs {
		hiddenOrgs[org] = hidden
	}
	incoming := make([]PR, len(app.incoming))
	copy(incoming, app.incoming)
	outgoing := make([]PR, len(app.outgoing))
	copy(outgoing, app.outgoing)
	app.mu.RUnlock()

	// Let the state manager figure out what needs notifications
	toNotify := app.stateManager.UpdatePRs(incoming, outgoing, hiddenOrgs)

	// Update deprecated fields for test compatibility
	app.mu.Lock()
	app.previousBlockedPRs = make(map[string]bool)
	app.blockedPRTimes = make(map[string]time.Time)
	states := app.stateManager.GetBlockedPRs()
	for url, state := range states {
		app.previousBlockedPRs[url] = true
		app.blockedPRTimes[url] = state.FirstBlockedAt
	}
	app.mu.Unlock()

	if len(toNotify) == 0 {
		log.Print("[NOTIFY] No PRs need notifications")
		return
	}

	log.Printf("[NOTIFY] %d PRs need notifications", len(toNotify))

	// Send notifications for each PR
	playedHonk := false
	playedRocket := false

	for i := range toNotify {
		pr := toNotify[i]
		isIncoming := false
		// Check if it's in the incoming list
		for j := range incoming {
			if incoming[j].URL == pr.URL {
				isIncoming = true
				break
			}
		}

		// Send notification
		if isIncoming {
			app.sendPRNotification(ctx, pr, "PR Blocked on You 🪿", "honk", &playedHonk)
		} else {
			// Add delay between different sound types
			if playedHonk && !playedRocket {
				time.Sleep(2 * time.Second)
			}
			app.sendPRNotification(ctx, pr, "Your PR is Blocked 🚀", "rocket", &playedRocket)
		}

		// Auto-open if enabled
		if app.enableAutoBrowser && time.Since(app.startTime) > startupGracePeriod {
			app.tryAutoOpenPR(ctx, pr, app.enableAutoBrowser, app.startTime)
		}
	}

	// Update menu if we sent notifications
	if len(toNotify) > 0 {
		log.Print("[NOTIFY] Updating menu after notifications")
		app.updateMenu(ctx)
	}
}

// sendPRNotification sends a notification for a single PR.
func (app *App) sendPRNotification(ctx context.Context, pr PR, title string, soundType string, playedSound *bool) {
	message := fmt.Sprintf("%s #%d: %s", pr.Repository, pr.Number, pr.Title)

	// Send desktop notification
	if err := beeep.Notify(title, message, ""); err != nil {
		log.Printf("[NOTIFY] Failed to send notification for %s: %v", pr.URL, err)
	}

	// Play sound (only once per type per cycle)
	if !*playedSound {
		log.Printf("[NOTIFY] Playing %s sound for PR: %s #%d", soundType, pr.Repository, pr.Number)
		app.playSound(ctx, soundType)
		*playedSound = true
	}
}

// updatePRStatesAndNotify is the simplified replacement for checkForNewlyBlockedPRs.
func (app *App) updatePRStatesAndNotify(ctx context.Context) {
	// Simple and clear: just process notifications
	app.processNotifications(ctx)
}
