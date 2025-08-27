package main

import "github.com/energye/systray"

// MenuItem is an interface for menu items that can be implemented by both
// real systray menu items and mock menu items for testing.
type MenuItem interface {
	Disable()
	Enable()
	Check()
	Uncheck()
	SetTitle(string)
	SetTooltip(string)
	Click(func())
	AddSubMenuItem(title, tooltip string) MenuItem
}

// RealMenuItem wraps a real systray.MenuItem to implement our MenuItem interface.
type RealMenuItem struct {
	*systray.MenuItem
}

// Ensure RealMenuItem implements MenuItem interface.
var _ MenuItem = (*RealMenuItem)(nil)

// Disable disables the menu item.
func (r *RealMenuItem) Disable() {
	r.MenuItem.Disable()
}

// Enable enables the menu item.
func (r *RealMenuItem) Enable() {
	r.MenuItem.Enable()
}

// Check checks the menu item.
func (r *RealMenuItem) Check() {
	r.MenuItem.Check()
}

// Uncheck unchecks the menu item.
func (r *RealMenuItem) Uncheck() {
	r.MenuItem.Uncheck()
}

// SetTitle sets the menu item title.
func (r *RealMenuItem) SetTitle(title string) {
	r.MenuItem.SetTitle(title)
}

// SetTooltip sets the menu item tooltip.
func (r *RealMenuItem) SetTooltip(tooltip string) {
	r.MenuItem.SetTooltip(tooltip)
}

// Click sets the click handler.
func (r *RealMenuItem) Click(handler func()) {
	r.MenuItem.Click(handler)
}

// AddSubMenuItem adds a sub menu item and returns it wrapped in our interface.
func (r *RealMenuItem) AddSubMenuItem(title, tooltip string) MenuItem {
	subItem := r.MenuItem.AddSubMenuItem(title, tooltip)
	return &RealMenuItem{MenuItem: subItem}
}

// MockMenuItem implements MenuItem for testing without calling systray functions.
type MockMenuItem struct {
	title        string
	tooltip      string
	disabled     bool
	checked      bool
	clickHandler func()
	subItems     []MenuItem
}

// Ensure MockMenuItem implements MenuItem interface.
var _ MenuItem = (*MockMenuItem)(nil)

// Disable marks the item as disabled.
func (m *MockMenuItem) Disable() {
	m.disabled = true
}

// Enable marks the item as enabled.
func (m *MockMenuItem) Enable() {
	m.disabled = false
}

// Check marks the item as checked.
func (m *MockMenuItem) Check() {
	m.checked = true
}

// Uncheck marks the item as unchecked.
func (m *MockMenuItem) Uncheck() {
	m.checked = false
}

// SetTitle sets the title.
func (m *MockMenuItem) SetTitle(title string) {
	m.title = title
}

// SetTooltip sets the tooltip.
func (m *MockMenuItem) SetTooltip(tooltip string) {
	m.tooltip = tooltip
}

// Click sets the click handler.
func (m *MockMenuItem) Click(handler func()) {
	m.clickHandler = handler
}

// AddSubMenuItem adds a sub menu item (mock).
func (m *MockMenuItem) AddSubMenuItem(title, tooltip string) MenuItem {
	subItem := &MockMenuItem{
		title:   title,
		tooltip: tooltip,
	}
	m.subItems = append(m.subItems, subItem)
	return subItem
}
