// Package theme holds the design tokens of docs/SPEC.md §9.2. Views take
// their colors from here, never from literals.
package theme

import "github.com/egoist/mygo/ui"

// Palette is the set of colors one appearance uses.
type Palette struct {
	Canvas, CanvasDot, Ink, Paper, Cream ui.Color
}

var (
	light = Palette{
		Canvas: ui.Hex("#F7F5F0"), CanvasDot: ui.Hex("#E4E0D8"),
		Ink: ui.Hex("#161616"), Paper: ui.Hex("#FFFFFF"), Cream: ui.Hex("#ECE8DF"),
	}
	dark = Palette{
		Canvas: ui.Hex("#1E2440"), CanvasDot: ui.Hex("#2C3354"),
		Ink: ui.Hex("#FFFFFF"), Paper: ui.Hex("#262D4D"), Cream: ui.Hex("#323A5E"),
	}
)

// Colors that stay the same in both appearances.
var (
	Coral = ui.Hex("#E8664A") // danger, alerts
	Amber = ui.Hex("#F2B63D") // warnings, renewal due
	Teal  = ui.Hex("#1FA7B8") // primary, links
	Green = ui.Hex("#2FA36B") // online, success
)

// WindowBackground is the canvas in both appearances, for
// mygo.WindowOptions.BackgroundColor, so the window opens in the page's color.
const WindowBackground = "light-dark(#F7F5F0, #1E2440)"

// For returns the palette of the window's current appearance.
func For(c *ui.Context) Palette {
	if c.Theme().Dark {
		return dark
	}
	return light
}

// Canvas fills an element with the page background: the canvas color with
// a 16px dot grid.
func Canvas(e ui.Element, p Palette) ui.Element {
	return e.Background(p.Canvas).Draw(func(pt *ui.Painter, r ui.Rect) {
		for y := r.Y + 8; y < r.Y+r.H; y += 16 {
			for x := r.X + 8; x < r.X+r.W; x += 16 {
				pt.Fill(ui.Rect{X: x, Y: y, W: 2, H: 2}, p.CanvasDot, 1)
			}
		}
	})
}
