//go:build (linux || freebsd || openbsd || netbsd || dragonfly || solaris || illumos || aix || windows) && !darwin

package main

import (
	_ "embed"
	"log/slog"
	"sync"

	"github.com/codeGROOVE-dev/goose/pkg/icon"
)

// Linux, BSD, and Windows use dynamic circle badges since they don't support title text.

//go:embed icons/smiling-face.png
var iconSmilingSource []byte

//go:embed icons/warning.png
var iconWarning []byte

//go:embed icons/lock.png
var iconLock []byte

var (
	cache = icon.NewCache()

	smiling     []byte
	smilingOnce sync.Once
)

func getIcon(iconType IconType, counts PRCounts) []byte {
	// Static icons for error states
	if iconType == IconWarning {
		return iconWarning
	}
	if iconType == IconLock {
		return iconLock
	}

	incoming := counts.IncomingBlocked
	outgoing := counts.OutgoingBlocked

	// Happy face when nothing is blocked
	if incoming == 0 && outgoing == 0 {
		smilingOnce.Do(func() {
			scaled, err := icon.Scale(iconSmilingSource)
			if err != nil {
				slog.Error("failed to scale happy face icon", "error", err)
				smiling = iconSmilingSource
				return
			}
			smiling = scaled
		})
		return smiling
	}

	// Check cache
	if cached, ok := cache.Lookup(incoming, outgoing); ok {
		return cached
	}

	// Generate badge
	badge, err := icon.Badge(incoming, outgoing)
	if err != nil {
		slog.Error("failed to generate badge", "error", err, "incoming", incoming, "outgoing", outgoing)
		return smiling
	}

	cache.Put(incoming, outgoing, badge)
	return badge
}
