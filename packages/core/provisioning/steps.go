package provisioning

import (
	"context"
	"errors"
	"fmt"
)

// host is what the hardening steps need from a server. The remote
// implementation runs commands over SSH; tests use a fake.
type host interface {
	ReadFile(ctx context.Context, path string) (content string, exists bool, err error)
	WriteFile(ctx context.Context, path, content string) error
	RemoveFile(ctx context.Context, path string) error
	TestSSHD(ctx context.Context) error
	ReloadSSHD(ctx context.Context) error
	UFW(ctx context.Context) (active bool, allowedTCP map[int]bool, err error)
	UFWAllowAndEnable(ctx context.Context, port int) error
	UFWDisable(ctx context.Context) error
}

// verifyFunc proves, over a new connection, that VPSGoGo can still log in
// and that the effective sshd configuration is hardened.
type verifyFunc func(ctx context.Context) error

var (
	// ErrSSHDConfigInvalid means `sshd -t` rejected the drop-in; sshd was
	// not reloaded and the previous file is back.
	ErrSSHDConfigInvalid = errors.New("provisioning: sshd rejected the configuration")
	// ErrSSHDRolledBack means the new configuration was loaded but the check
	// over a new connection failed, so the previous file was restored.
	ErrSSHDRolledBack = errors.New("provisioning: sshd change rolled back")
)

const sshdDropInPath = "/etc/ssh/sshd_config.d/10-vpsgogo.conf"

// sshdDropIn sorts before cloud-init's and most providers' drop-ins; sshd
// keeps the first value it reads, so verify still checks the effective
// configuration.
const sshdDropIn = `# Managed by VPSGoGo. Key-only login, no root login.
PasswordAuthentication no
KbdInteractiveAuthentication no
PermitRootLogin no
`

// ensureFile writes content to path unless it is already there.
func ensureFile(ctx context.Context, h host, path, content string) (changed bool, err error) {
	cur, exists, err := h.ReadFile(ctx, path)
	if err != nil {
		return false, err
	}
	if exists && cur == content {
		return false, nil
	}
	return true, h.WriteFile(ctx, path, content)
}

func hardenSSH(ctx context.Context, h host, dropIn string, verify verifyFunc) (changed bool, err error) {
	prev, existed, err := h.ReadFile(ctx, sshdDropInPath)
	if err != nil {
		return false, err
	}
	if existed && prev == dropIn {
		if err := verify(ctx); err != nil {
			return false, fmt.Errorf("provisioning: sshd drop-in is in place but not in effect: %w", err)
		}
		return false, nil
	}

	restore := func() error {
		if existed {
			return h.WriteFile(ctx, sshdDropInPath, prev)
		}
		return h.RemoveFile(ctx, sshdDropInPath)
	}

	if err := h.WriteFile(ctx, sshdDropInPath, dropIn); err != nil {
		return false, err
	}
	if err := h.TestSSHD(ctx); err != nil {
		if rerr := restore(); rerr != nil {
			return false, errors.Join(ErrSSHDConfigInvalid, err, rerr)
		}
		return false, fmt.Errorf("%w: %w", ErrSSHDConfigInvalid, err)
	}
	if err := h.ReloadSSHD(ctx); err != nil {
		return false, errors.Join(err, restore())
	}
	if err := verify(ctx); err != nil {
		rerr := restore()
		if rerr == nil {
			rerr = h.ReloadSSHD(ctx)
		}
		if rerr != nil {
			return false, errors.Join(ErrSSHDRolledBack, err, rerr)
		}
		return false, fmt.Errorf("%w: %w", ErrSSHDRolledBack, err)
	}
	return true, nil
}

// ensureFirewall allows sshPort (the port sshd listens on, which behind NAT
// is not the one the user dials) and then enables ufw. If a new connection
// fails afterwards, ufw is disabled again over the existing one.
func ensureFirewall(ctx context.Context, h host, sshPort int, verify verifyFunc) (changed bool, err error) {
	active, allowed, err := h.UFW(ctx)
	if err != nil {
		return false, err
	}
	if active && allowed[sshPort] {
		return false, nil
	}
	if err := h.UFWAllowAndEnable(ctx, sshPort); err != nil {
		return false, err
	}
	if err := verify(ctx); err != nil {
		return false, errors.Join(fmt.Errorf("provisioning: firewall disabled again: %w", err), h.UFWDisable(ctx))
	}
	return true, nil
}
