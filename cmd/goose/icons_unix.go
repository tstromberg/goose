//go:build !windows

package main

import (
	_ "embed"
)

// Embed PNG files for Unix-like systems (macOS, Linux)
//
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
