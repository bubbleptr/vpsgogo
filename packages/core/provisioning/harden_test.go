package provisioning

import (
	"context"
	"errors"
	"testing"
)

// fakeHost is a server's files plus a log of the service actions taken.
type fakeHost struct {
	files      map[string]string
	sshdValid  bool
	reloads    int
	ufwActive  bool
	ufwAllowed map[int]bool
}

func newFakeHost() *fakeHost {
	return &fakeHost{files: map[string]string{}, sshdValid: true, ufwAllowed: map[int]bool{}}
}

func (h *fakeHost) ReadFile(_ context.Context, path string) (string, bool, error) {
	s, ok := h.files[path]
	return s, ok, nil
}
func (h *fakeHost) WriteFile(_ context.Context, path, content string) error {
	h.files[path] = content
	return nil
}
func (h *fakeHost) RemoveFile(_ context.Context, path string) error {
	delete(h.files, path)
	return nil
}
func (h *fakeHost) TestSSHD(context.Context) error {
	if !h.sshdValid {
		return errors.New("sshd -t: bad configuration option")
	}
	return nil
}
func (h *fakeHost) ReloadSSHD(context.Context) error { h.reloads++; return nil }
func (h *fakeHost) UFW(context.Context) (bool, map[int]bool, error) {
	return h.ufwActive, h.ufwAllowed, nil
}
func (h *fakeHost) UFWAllowAndEnable(_ context.Context, port int) error {
	h.ufwAllowed[port] = true
	h.ufwActive = true
	return nil
}
func (h *fakeHost) UFWDisable(context.Context) error { h.ufwActive = false; return nil }

var errLockedOut = errors.New("new login failed")

func ok(context.Context) error      { return nil }
func lockout(context.Context) error { return errLockedOut }

func TestHardenSSHApplies(t *testing.T) {
	h := newFakeHost()
	changed, err := hardenSSH(context.Background(), h, sshdDropIn, ok)
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	if h.files[sshdDropInPath] != sshdDropIn || h.reloads != 1 {
		t.Fatalf("file=%q reloads=%d", h.files[sshdDropInPath], h.reloads)
	}
}

func TestHardenSSHUnchangedWhenAlreadyApplied(t *testing.T) {
	h := newFakeHost()
	h.files[sshdDropInPath] = sshdDropIn
	changed, err := hardenSSH(context.Background(), h, sshdDropIn, ok)
	if err != nil || changed || h.reloads != 0 {
		t.Fatalf("changed=%v err=%v reloads=%d", changed, err, h.reloads)
	}
}

func TestHardenSSHInvalidConfigNeverReloads(t *testing.T) {
	h := newFakeHost()
	h.sshdValid = false
	_, err := hardenSSH(context.Background(), h, "NotAnOption yes\n", ok)
	if !errors.Is(err, ErrSSHDConfigInvalid) {
		t.Fatalf("err = %v", err)
	}
	if _, exists := h.files[sshdDropInPath]; exists || h.reloads != 0 {
		t.Fatalf("left file=%v reloads=%d", exists, h.reloads)
	}
}

func TestHardenSSHRollsBackWhenNewLoginFails(t *testing.T) {
	h := newFakeHost()
	h.files[sshdDropInPath] = "# earlier version\n"
	_, err := hardenSSH(context.Background(), h, "AllowUsers nobody\n", lockout)
	if !errors.Is(err, ErrSSHDRolledBack) || !errors.Is(err, errLockedOut) {
		t.Fatalf("err = %v", err)
	}
	if h.files[sshdDropInPath] != "# earlier version\n" {
		t.Fatalf("not restored: %q", h.files[sshdDropInPath])
	}
	if h.reloads != 2 {
		t.Fatalf("reloads = %d, want 2 (apply, then restore)", h.reloads)
	}
}

func TestHardenSSHAlreadyAppliedButNotEffectiveFails(t *testing.T) {
	h := newFakeHost()
	h.files[sshdDropInPath] = sshdDropIn
	if _, err := hardenSSH(context.Background(), h, sshdDropIn, lockout); err == nil {
		t.Fatal("reported success although the effective config is not hardened")
	}
}

func TestFirewallAllowsSSHBeforeEnabling(t *testing.T) {
	h := newFakeHost()
	changed, err := ensureFirewall(context.Background(), h, 22, ok)
	if err != nil || !changed || !h.ufwActive || !h.ufwAllowed[22] {
		t.Fatalf("changed=%v err=%v host=%+v", changed, err, h)
	}
	changed, err = ensureFirewall(context.Background(), h, 22, ok)
	if err != nil || changed {
		t.Fatalf("second run changed=%v err=%v", changed, err)
	}
}

func TestFirewallDisabledAgainWhenNewLoginFails(t *testing.T) {
	h := newFakeHost()
	_, err := ensureFirewall(context.Background(), h, 22, lockout)
	if !errors.Is(err, errLockedOut) || h.ufwActive {
		t.Fatalf("err=%v active=%v", err, h.ufwActive)
	}
}
