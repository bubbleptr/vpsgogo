package fake

import (
	"context"
	"testing"

	"github.com/bubbleptr/vpsgogo/packages/core/fleet"
)

func TestAddServerSkipsSeededIDs(t *testing.T) {
	b := New()
	b.Seed(fleet.Server{ID: "srv-1", Name: "old"})
	b.Seed(fleet.Server{ID: "srv-3", Name: "older"})
	seen := map[string]bool{"srv-1": true, "srv-3": true}
	for range 3 {
		s, err := b.AddServer(context.Background(), fleet.NewServer{Host: "203.0.113.7"}, "fp")
		if err != nil {
			t.Fatal(err)
		}
		if seen[s.ID] {
			t.Fatalf("AddServer reused ID %q", s.ID)
		}
		seen[s.ID] = true
	}
}
