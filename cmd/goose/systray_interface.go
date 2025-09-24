package main

import (
	"github.com/energye/systray"
)

// SystrayInterface abstracts systray operations for testing.
type SystrayInterface interface {
	ResetMenu()
	AddMenuItem(title, tooltip string) MenuItem
	AddSeparator()
	SetTitle(title string)
	SetIcon(iconBytes []byte)
	SetOnClick(fn func(menu systray.IMenu))
	Quit()
}

// RealSystray implements SystrayInterface using the actual systray library.
type RealSystray struct{}

func (*RealSystray) ResetMenu() {
	systray.ResetMenu()
}

func (*RealSystray) AddMenuItem(title, tooltip string) MenuItem {
	item := systray.AddMenuItem(title, tooltip)
	return &RealMenuItem{MenuItem: item}
}

func (*RealSystray) AddSeparator() {
	systray.AddSeparator()
}

func (*RealSystray) SetTitle(title string) {
	systray.SetTitle(title)
}

func (*RealSystray) SetIcon(iconBytes []byte) {
	systray.SetIcon(iconBytes)
}

func (*RealSystray) SetOnClick(fn func(menu systray.IMenu)) {
	systray.SetOnClick(fn)
}

func (*RealSystray) Quit() {
	systray.Quit()
}

// MockSystray implements SystrayInterface for testing.
type MockSystray struct {
	title     string
	menuItems []string
}

func (m *MockSystray) ResetMenu() {
	m.menuItems = nil
}

func (m *MockSystray) AddMenuItem(title, tooltip string) MenuItem {
	m.menuItems = append(m.menuItems, title)
	// Return a MockMenuItem that won't panic when methods are called
	return &MockMenuItem{
		title:   title,
		tooltip: tooltip,
	}
}

func (m *MockSystray) AddSeparator() {
	m.menuItems = append(m.menuItems, "---")
}

func (m *MockSystray) SetTitle(title string) {
	m.title = title
}

func (*MockSystray) SetIcon(iconBytes []byte) {
	// No-op for testing
}

func (*MockSystray) SetOnClick(_ func(menu systray.IMenu)) {
	// No-op for testing
}

func (*MockSystray) Quit() {
	// No-op for testing
}
