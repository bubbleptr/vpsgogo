// Package dashboard is the app's first page.
package dashboard

import (
	"fmt"

	"github.com/bubbleptr/vpsgogo/apps/desktop/services"
	"github.com/bubbleptr/vpsgogo/apps/desktop/shared/theme"
	"github.com/egoist/mygo/ui"
)

// Page shows what the app knows about itself and, later, the fleet.
type Page struct {
	info services.AppInfo
}

// New returns the page for an app described by info.
func New(info services.AppInfo) *Page { return &Page{info: info} }

// View builds the page.
func (p *Page) View(c *ui.Context) {
	pal := theme.For(c)
	theme.Canvas(ui.Column(c).Fill(), pal).Padding(32).Gap(8).Children(func() {
		ui.Text(c, "VPSGoGo").FontSize(26).FontWeight(800).TextColor(pal.Ink)
		ui.Text(c, fmt.Sprintf("v%s · %s/%s", p.info.Version, p.info.OS, p.info.Arch)).TextColor(pal.Ink).Opacity(.6)
	})
}
