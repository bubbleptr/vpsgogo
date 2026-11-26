package fleet

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/bubbleptr/vpsgogo/packages/core/vault"
)

const fp = "SHA256:abcdefghijklmnopqrstuvwxyz0123456789ABCDEFG"

func newService(t *testing.T) (*Service, *vault.Memory) {
	t.Helper()
	ctx := context.Background()
	repo, err := OpenSQLite(ctx, filepath.Join(t.TempDir(), "fleet.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { repo.Close() })
	v := vault.NewMemory()
	return NewService(repo, v), v
}

func TestNewServerValidation(t *testing.T) {
	cases := []struct {
		name string
		in   NewServer
		ok   bool
	}{
		{"ipv4", NewServer{Host: "203.0.113.7"}, true},
		{"ipv6", NewServer{Host: "2001:db8::1"}, true},
		{"hostname", NewServer{Host: "vps-1.example.com"}, true},
		{"empty host", NewServer{Host: " "}, false},
		{"url instead of host", NewServer{Host: "ssh://203.0.113.7"}, false},
		{"host with port", NewServer{Host: "203.0.113.7:22"}, false},
		{"space in host", NewServer{Host: "vps 1"}, false},
		{"port too big", NewServer{Host: "203.0.113.7", Port: 70000}, false},
		{"negative port", NewServer{Host: "203.0.113.7", Port: -1}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := c.in.normalize()
			if (err == nil) != c.ok {
				t.Fatalf("normalize(%+v) err = %v, want ok=%v", c.in, err, c.ok)
			}
		})
	}
}

func TestAddDefaultsAndPersists(t *testing.T) {
	ctx := context.Background()
	svc, _ := newService(t)

	s, err := svc.Add(ctx, NewServer{Host: " 203.0.113.7 "}, fp)
	if err != nil {
		t.Fatal(err)
	}
	if s.Port != 22 || s.Name != "203.0.113.7" || s.Host != "203.0.113.7" || s.User != "root" || s.HostKeyFP != fp {
		t.Fatalf("got %+v", s)
	}

	got, err := svc.Get(ctx, s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got != s {
		t.Fatalf("Get = %+v, want %+v", got, s)
	}
}

func TestAddRequiresConfirmedFingerprint(t *testing.T) {
	svc, _ := newService(t)
	if _, err := svc.Add(context.Background(), NewServer{Host: "203.0.113.7"}, ""); err == nil {
		t.Fatal("added a server without a confirmed host key")
	}
}

func TestListKeepsInsertionOrder(t *testing.T) {
	ctx := context.Background()
	svc, _ := newService(t)
	for _, h := range []string{"a.example.com", "b.example.com", "c.example.com"} {
		if _, err := svc.Add(ctx, NewServer{Host: h}, fp); err != nil {
			t.Fatal(err)
		}
	}
	list, err := svc.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 3 || list[0].Host != "a.example.com" || list[2].Host != "c.example.com" {
		t.Fatalf("List = %+v", list)
	}
}

func TestGetMissingServer(t *testing.T) {
	svc, _ := newService(t)
	if _, err := svc.Get(context.Background(), "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestDeleteRemovesServerAndItsKey(t *testing.T) {
	ctx := context.Background()
	svc, v := newService(t)
	s, err := svc.Add(ctx, NewServer{Host: "203.0.113.7"}, fp)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.SetPrivateKey(s.ID, []byte("-----BEGIN OPENSSH PRIVATE KEY-----")); err != nil {
		t.Fatal(err)
	}

	if err := svc.Delete(ctx, s.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Get(ctx, s.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("server still there: %v", err)
	}
	if _, err := v.Get(privateKeyRef(s.ID)); !errors.Is(err, vault.ErrNotFound) {
		t.Fatalf("private key still in the vault: %v", err)
	}
}
