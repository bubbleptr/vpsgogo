package dashboard

import (
	"testing"

	"github.com/bubbleptr/vpsgogo/apps/desktop/services"
	"github.com/egoist/mygo/ui"
)

func TestShowsAppInfo(t *testing.T) {
	p := New(services.AppInfo{Version: "0.1.0-test", OS: "darwin", Arch: "arm64"})
	for _, dark := range []bool{false, true} {
		tt := ui.NewTesterWith(p.View, 1200, 780, ui.TesterOptions{Dark: dark})
		for _, want := range []string{"VPSGoGo", "v0.1.0-test · darwin/arm64"} {
			if !tt.HasText(want) {
				t.Errorf("dark=%v: no %q among %q", dark, want, tt.Texts())
			}
		}
	}
}
