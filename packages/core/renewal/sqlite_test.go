package renewal

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/bubbleptr/vpsgogo/packages/core/fleet"
)

func TestSQLiteKeepsSubscriptionsBesideTheFleet(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "vpsgogo.db")
	// The desktop app keeps servers and plans in one file.
	servers, err := fleet.OpenSQLite(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer servers.Close()
	db, err := OpenSQLite(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := servers.Insert(ctx, fleet.Server{ID: "s1", Name: "vps", Host: "203.0.113.1", Port: 22, User: "root", HostKeyFP: "SHA256:x"}); err != nil {
		t.Fatal(err)
	}

	sub := Subscription{ServerID: "s1", ExpiresAt: d("2027-01-31"), Cycle: Monthly, Price: "5.00", Currency: "USD", RemindDays: []int{5, 0}}
	if err := db.Put(ctx, sub); err != nil {
		t.Fatal(err)
	}
	if err := db.Put(ctx, Subscription{ServerID: "s2", Cycle: Monthly}); err == nil {
		t.Error("stored a plan with no expiry")
	}
	got, err := db.Renew(ctx, "s1")
	if err != nil || got.ExpiresAt != d("2027-02-28") {
		t.Fatalf("Renew: %+v %v", got, err)
	}
	db.Close()

	db, err = OpenSQLite(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	all, err := db.List(ctx)
	if err != nil || len(all) != 1 {
		t.Fatalf("List: %+v %v", all, err)
	}
	if s := all[0]; s.ExpiresAt != d("2027-02-28") || s.BillingDay != 31 || s.Price != "5.00" || len(s.RemindDays) != 2 {
		t.Errorf("after reopening: %+v", s)
	}
	if _, err := servers.Get(ctx, "s1"); err != nil {
		t.Errorf("the fleet table: %v", err)
	}
	if err := db.Delete(ctx, "s1"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Get(ctx, "s1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get after Delete: %v", err)
	}
	if _, err := db.Renew(ctx, "s1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Renew after Delete: %v", err)
	}
}
