package provisioning

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bubbleptr/vpsgogo/packages/core/fleet"
	"github.com/bubbleptr/vpsgogo/packages/core/sshx"
	"github.com/bubbleptr/vpsgogo/packages/core/vault"
	"golang.org/x/crypto/ssh"
)

// scriptedSession answers the read-only commands the pipeline starts with.
type scriptedSession struct{}

func (scriptedSession) Exec(_ context.Context, cmd string) (sshx.ExecResult, error) {
	switch {
	case strings.Contains(cmd, "os-release"):
		return sshx.ExecResult{Stdout: "ID=debian\nVERSION_ID=\"12\"\n"}, nil
	case strings.Contains(cmd, "dpkg-query"):
		return sshx.ExecResult{Stdout: strings.Repeat("installed\n", len(basePackages))}, nil
	}
	return sshx.ExecResult{Code: 1, Stderr: "unexpected: " + cmd}, nil
}
func (scriptedSession) Close() error { return nil }

// lockedVault is a keychain that cannot be read right now.
type lockedVault struct{ sets int }

func (v *lockedVault) Get(string) ([]byte, error) { return nil, errors.New("keychain is locked") }
func (v *lockedVault) Set(string, []byte) error   { v.sets++; return nil }
func (v *lockedVault) Delete(string) error        { return nil }

var _ vault.Vault = (*lockedVault)(nil)

func TestUnreadableKeyIsNotReplaced(t *testing.T) {
	ctx := context.Background()
	repo, err := fleet.OpenSQLite(ctx, filepath.Join(t.TempDir(), "fleet.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	v := &lockedVault{}
	fl := fleet.NewService(repo, v)
	srv, err := fl.Add(ctx, fleet.NewServer{Host: "203.0.113.7"}, "SHA256:x")
	if err != nil {
		t.Fatal(err)
	}

	p := &Provisioner{Fleet: fl, Dial: func(context.Context, fleet.Server, string, ssh.AuthMethod) (Session, error) {
		return scriptedSession{}, nil
	}}
	if err := p.Run(ctx, srv.ID, "pw"); err == nil {
		t.Fatal("ran on although the stored key could not be read")
	}
	if v.sets != 0 {
		t.Fatalf("replaced the stored key %d time(s)", v.sets)
	}
}
