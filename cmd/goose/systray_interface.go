package main

import "github.com/energye/systray"

// SystrayInterface abstracts systray operations for testing
type SystrayInterface interface {
	ResetMenu()
	AddMenuItem(title, tooltip string) *systray.MenuItem
	AddSeparator()
	SetTitle(title string)
	SetOnClick(fn func(menu systray.IMenu))
	Quit()
}

// RealSystray implements SystrayInterface using the actual systray library
type RealSystray struct{}

func (r *RealSystray) ResetMenu() {
	systray.ResetMenu()
}

func (r *RealSystray) AddMenuItem(title, tooltip string) *systray.MenuItem {
	return systray.AddMenuItem(title, tooltip)
}

func (r *RealSystray) AddSeparator() {
	systray.AddSeparator()
}

func (r *RealSystray) SetTitle(title string) {
	systray.SetTitle(title)
}

func (r *RealSystray) SetOnClick(fn func(menu systray.IMenu)) {
	systray.SetOnClick(fn)
}

func (r *RealSystray) Quit() {
	systray.Quit()
}

// MockSystray implements SystrayInterface for testing
type MockSystray struct {
	title     string
	menuItems []string
}

func (m *MockSystray) ResetMenu() {
	m.menuItems = nil
}

func (m *MockSystray) AddMenuItem(title, tooltip string) *systray.MenuItem {
	m.menuItems = append(m.menuItems, title)
	return &systray.MenuItem{} // Return empty menu item for testing
}

func (m *MockSystray) AddSeparator() {
	m.menuItems = append(m.menuItems, "---")
}

func (m *MockSystray) SetTitle(title string) {
	m.title = title
}

func (m *MockSystray) SetOnClick(fn func(menu systray.IMenu)) {
	// No-op for testing
}

func (m *MockSystray) Quit() {
	// No-op for testing
}