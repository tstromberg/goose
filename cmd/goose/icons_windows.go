//go:build windows

package main

import (
	_ "embed"
)

// Embed Windows ICO files at compile time
//
//go:embed icons/goose.ico
var iconGoose []byte

//go:embed icons/popper.ico
var iconPopper []byte

//go:embed icons/smiling-face.ico
var iconSmiling []byte

//go:embed icons/warning.ico
var iconWarning []byte

// lock.ico not yet created, using warning as fallback
var iconLock = iconWarning