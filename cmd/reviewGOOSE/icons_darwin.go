//go:build darwin

package main

import _ "embed"

// macOS displays counts in the title bar, so icons remain static.

//go:embed icons/goose.png
var iconGoose []byte

//go:embed icons/popper.png
var iconPopper []byte

//go:embed icons/smiling-face.png
var iconSmiling []byte

//go:embed icons/lock.png
var iconLock []byte

//go:embed icons/warning.png
var iconWarning []byte

//go:embed icons/cockroach.png
var iconCockroach []byte

func getIcon(iconType IconType, _ PRCounts) []byte {
	switch iconType {
	case IconGoose, IconBoth:
		return iconGoose
	case IconPopper:
		return iconPopper
	case IconCockroach:
		return iconCockroach
	case IconWarning:
		return iconWarning
	case IconLock:
		return iconLock
	default:
		return iconSmiling
	}
}
