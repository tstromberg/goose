// Package icon generates system tray icons for the Goose application.
//
// On platforms that don't support dynamic title text (Linux, Windows),
// icons are rendered as colored circle badges with white numbers:
//   - Red circle: incoming PRs needing review
//   - Green circle: outgoing PRs blocked
//   - Both: red (top-left) + green (bottom-right)
//
// Generated icons are 48×48 pixels for optimal display on KDE and GNOME.
package icon

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"strconv"
	"sync"

	"golang.org/x/image/draw"
	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gomonobold"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

// Size is the standard system tray icon size (48×48 for KDE/GNOME).
const Size = 48

// Color scheme for PR state indicators.
var (
	red   = color.RGBA{220, 53, 69, 255}   // Incoming PRs (needs attention)
	green = color.RGBA{40, 167, 69, 255}   // Outgoing PRs (in progress)
	white = color.RGBA{255, 255, 255, 255} // Text color
)

// Badge generates a badge icon showing PR counts.
//
// Visual design for accessibility:
//   - Incoming only: Red CIRCLE (helps color-blind users distinguish)
//   - Outgoing only: Green SQUARE
//   - Both: Diagonal split (red top-left, green bottom-right)
//
// Returns nil if both counts are zero (caller should use happy face icon).
// Numbers are capped at 99 for display purposes.
func Badge(incoming, outgoing int) ([]byte, error) {
	if incoming == 0 && outgoing == 0 {
		return nil, nil
	}

	img := image.NewRGBA(image.Rect(0, 0, Size, Size))

	switch {
	case incoming > 0 && outgoing > 0:
		// Both: diagonal split with bold numbers
		drawDiagonalSplit(img, format(incoming), format(outgoing))
	case incoming > 0:
		// Incoming only: large red circle with bold number
		drawCircle(img, red, format(incoming))
	default:
		// Outgoing only: large green square with bold number
		drawSquare(img, green, format(outgoing))
	}

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, fmt.Errorf("encode png: %w", err)
	}
	return buf.Bytes(), nil
}

// Scale resizes an icon to the standard tray size.
func Scale(iconData []byte) ([]byte, error) {
	src, err := png.Decode(bytes.NewReader(iconData))
	if err != nil {
		return nil, fmt.Errorf("decode png: %w", err)
	}

	dst := image.NewRGBA(image.Rect(0, 0, Size, Size))
	draw.NearestNeighbor.Scale(dst, dst.Bounds(), src, src.Bounds(), draw.Over, nil)

	var buf bytes.Buffer
	if err := png.Encode(&buf, dst); err != nil {
		return nil, fmt.Errorf("encode png: %w", err)
	}
	return buf.Bytes(), nil
}

// drawCircle renders a large filled circle with bold centered text.
func drawCircle(img *image.RGBA, fill color.RGBA, text string) {
	radius := float64(Size) / 2
	cx := radius
	cy := radius

	// Draw filled circle
	for py := range Size {
		for px := range Size {
			dx := float64(px) - cx + 0.5
			dy := float64(py) - cy + 0.5
			dist := math.Sqrt(dx*dx + dy*dy)

			if dist <= radius {
				img.Set(px, py, fill)
			}
		}
	}

	// Draw large bold centered text
	drawBoldText(img, text, Size/2, Size/2)
}

// drawSquare renders a solid square with bold centered text.
func drawSquare(img *image.RGBA, fill color.RGBA, text string) {
	// Fill entire image with color
	for py := range Size {
		for px := range Size {
			img.Set(px, py, fill)
		}
	}

	// Draw large bold centered text
	drawBoldText(img, text, Size/2, Size/2)
}

// drawDiagonalSplit renders a diagonal split with two numbers.
func drawDiagonalSplit(img *image.RGBA, incomingText, outgoingText string) {
	// Fill with diagonal split: red top-left, green bottom-right
	for py := range Size {
		for px := range Size {
			if px < Size-py {
				img.Set(px, py, red)
			} else {
				img.Set(px, py, green)
			}
		}
	}

	// Draw incoming number in top-left quadrant (lowered 1 pixel)
	drawBoldText(img, incomingText, Size/4, Size/4+1)

	// Draw outgoing number in bottom-right quadrant (raised 1 pixel)
	drawBoldText(img, outgoingText, 3*Size/4, 3*Size/4-1)
}

// drawBoldText renders large, professional text using Go's monospace bold font.
func drawBoldText(img *image.RGBA, text string, centerX, centerY int) {
	// Parse Go's embedded monospace bold font
	face, err := opentype.Parse(gomonobold.TTF)
	if err != nil {
		return // Graceful fallback: show colored badge without text
	}

	// Create font face at large size (32 points = ~42 pixels tall)
	fontSize := 32.0
	fontFace, err := opentype.NewFace(face, &opentype.FaceOptions{
		Size: fontSize,
		DPI:  72,
	})
	if err != nil {
		return
	}
	defer fontFace.Close() //nolint:errcheck // Close error is not critical for rendering

	// Measure text bounds
	bounds, advance := font.BoundString(fontFace, text)
	textWidth := advance.Ceil()

	// Calculate baseline position to center text vertically
	// The visual center of text is at (bounds.Max.Y + bounds.Min.Y) / 2 above baseline
	// So baseline Y = centerY - visualCenter
	visualCenter := (bounds.Max.Y + bounds.Min.Y) / 2
	baselineY := fixed.I(centerY) - visualCenter

	// Center horizontally
	x := fixed.I(centerX - textWidth/2)

	// Draw the text
	drawer := &font.Drawer{
		Dst:  img,
		Src:  image.NewUniform(white),
		Face: fontFace,
		Dot:  fixed.Point26_6{X: x, Y: baselineY},
	}
	drawer.DrawString(text)
}

// format converts a count to display text.
// Shows single digits 1-9, or "+" for 10 or more.
func format(n int) string {
	if n > 9 {
		return "+"
	}
	return strconv.Itoa(n)
}

// Cache stores generated icons to avoid redundant rendering.
type Cache struct {
	icons map[string][]byte
	mu    sync.RWMutex
}

// NewCache creates an icon cache.
func NewCache() *Cache {
	return &Cache{icons: make(map[string][]byte)}
}

// Lookup retrieves a cached icon or returns false if not found.
func (c *Cache) Lookup(in, out int) ([]byte, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	data, ok := c.icons[key(in, out)]
	return data, ok
}

// Put stores an icon in the cache.
func (c *Cache) Put(incoming, outgoing int, data []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Simple size limit
	if len(c.icons) > 100 {
		clear(c.icons)
	}

	c.icons[key(incoming, outgoing)] = data
}

func key(incoming, outgoing int) string {
	return strconv.Itoa(incoming) + ":" + strconv.Itoa(outgoing)
}
