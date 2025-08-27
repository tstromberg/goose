package main

import (
	"reflect"

	"github.com/energye/systray"
)

// SystrayInterface abstracts systray operations for testing.
type SystrayInterface interface {
	ResetMenu()
	AddMenuItem(title, tooltip string) *systray.MenuItem
	AddSeparator()
	SetTitle(title string)
	SetOnClick(fn func(menu systray.IMenu))
	Quit()
}

// RealSystray implements SystrayInterface using the actual systray library.
type RealSystray struct{}

func (*RealSystray) ResetMenu() {
	systray.ResetMenu()
}

func (*RealSystray) AddMenuItem(title, tooltip string) *systray.MenuItem {
	return systray.AddMenuItem(title, tooltip)
}

func (*RealSystray) AddSeparator() {
	systray.AddSeparator()
}

func (*RealSystray) SetTitle(title string) {
	systray.SetTitle(title)
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

func (m *MockSystray) AddMenuItem(title, _ string) *systray.MenuItem {
	m.menuItems = append(m.menuItems, title)
	// Create a MenuItem with initialized internal maps using reflection
	// This prevents nil map panics when methods are called
	item := &systray.MenuItem{}

	// Use reflection to initialize internal maps if they exist
	// This is a hack but necessary for testing
	rv := reflect.ValueOf(item).Elem()
	rt := rv.Type()
	for i := 0; i < rt.NumField(); i++ {
		field := rv.Field(i)
		if field.Kind() == reflect.Map && field.IsNil() && field.CanSet() {
			field.Set(reflect.MakeMap(field.Type()))
		}
	}

	return item
}

func (m *MockSystray) AddSeparator() {
	m.menuItems = append(m.menuItems, "---")
}

func (m *MockSystray) SetTitle(title string) {
	m.title = title
}

func (*MockSystray) SetOnClick(_ func(menu systray.IMenu)) {
	// No-op for testing
}

func (*MockSystray) Quit() {
	// No-op for testing
}
