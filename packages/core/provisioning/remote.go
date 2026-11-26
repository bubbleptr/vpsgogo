package provisioning

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"path"
	"strconv"
	"strings"

	"github.com/bubbleptr/vpsgogo/packages/core/sshx"
)

// Session is a connection that runs commands; *sshx.Client is one.
type Session interface {
	Exec(ctx context.Context, cmd string) (sshx.ExecResult, error)
	Close() error
}

// remote is the host implementation over an SSH session. Commands run as
// root: directly when logged in as root, otherwise through `sudo -n`.
type remote struct {
	s    Session
	root bool
	log  io.Writer
}

// exec runs script with root privileges and returns its result, logging the
// command and its output. The root password never appears in a command.
func (h *remote) exec(ctx context.Context, script string) (sshx.ExecResult, error) {
	cmd := "sh -c " + shellQuote(script)
	if !h.root {
		cmd = "sudo -n " + cmd
	}
	res, err := h.s.Exec(ctx, cmd)
	fmt.Fprintf(h.log, "$ %s\n", script)
	if err != nil {
		fmt.Fprintf(h.log, "! %v\n", err)
		return res, err
	}
	fmt.Fprintf(h.log, "[exit %d]\n%s%s", res.Code, clip(res.Stdout), clip(res.Stderr))
	return res, nil
}

// run is exec that treats a non-zero exit as an error.
func (h *remote) run(ctx context.Context, script string) (string, error) {
	res, err := h.exec(ctx, script)
	if err != nil {
		return "", err
	}
	if res.Code != 0 {
		return res.Stdout, fmt.Errorf("provisioning: %q exited %d: %s", firstLine(script), res.Code, strings.TrimSpace(res.Stderr))
	}
	return res.Stdout, nil
}

func (h *remote) ReadFile(ctx context.Context, p string) (string, bool, error) {
	q := shellQuote(p)
	res, err := h.exec(ctx, "if [ -f "+q+" ]; then cat "+q+"; else exit 3; fi")
	switch {
	case err != nil:
		return "", false, err
	case res.Code == 3:
		return "", false, nil
	case res.Code != 0:
		return "", false, fmt.Errorf("provisioning: read %s: %s", p, strings.TrimSpace(res.Stderr))
	}
	return res.Stdout, true, nil
}

func (h *remote) WriteFile(ctx context.Context, p, content string) error {
	q, tmp := shellQuote(p), shellQuote(p+".vpsgogo-tmp")
	_, err := h.run(ctx, fmt.Sprintf("mkdir -p %s && printf %%s %s | base64 -d > %s && chmod 644 %s && mv %s %s",
		shellQuote(path.Dir(p)), base64.StdEncoding.EncodeToString([]byte(content)), tmp, tmp, tmp, q))
	return err
}

func (h *remote) RemoveFile(ctx context.Context, p string) error {
	_, err := h.run(ctx, "rm -f "+shellQuote(p))
	return err
}

func (h *remote) TestSSHD(ctx context.Context) error {
	_, err := h.run(ctx, "/usr/sbin/sshd -t")
	return err
}

func (h *remote) ReloadSSHD(ctx context.Context) error {
	_, err := h.run(ctx, "systemctl reload ssh || systemctl reload sshd")
	return err
}

func (h *remote) UFW(ctx context.Context) (bool, map[int]bool, error) {
	out, err := h.run(ctx, "ufw status")
	if err != nil {
		return false, nil, err
	}
	return parseUFWStatus(out)
}

func (h *remote) UFWAllowAndEnable(ctx context.Context, port int) error {
	_, err := h.run(ctx, fmt.Sprintf("ufw allow %d/tcp && ufw --force enable", port))
	return err
}

func (h *remote) UFWDisable(ctx context.Context) error {
	_, err := h.run(ctx, "ufw --force disable")
	return err
}

// sshdEffective returns `sshd -T`: the configuration sshd actually uses.
func (h *remote) sshdEffective(ctx context.Context) (map[string]string, error) {
	out, err := h.run(ctx, "/usr/sbin/sshd -T")
	if err != nil {
		return nil, err
	}
	return parseSSHDConfig(out), nil
}

func parseSSHDConfig(out string) map[string]string {
	m := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), " ")
		if ok {
			if _, seen := m[k]; !seen {
				m[k] = v
			}
		}
	}
	return m
}

func parseUFWStatus(out string) (bool, map[int]bool, error) {
	allowed := map[int]bool{}
	active := false
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && f[0] == "Status:" {
			active = f[1] == "active"
		}
		if len(f) >= 2 && f[1] == "ALLOW" && !strings.Contains(line, "(v6)") {
			if p, ok := strings.CutSuffix(f[0], "/tcp"); ok {
				if n, err := strconv.Atoi(p); err == nil {
					allowed[n] = true
				}
			}
		}
	}
	return active, allowed, nil
}

func clip(s string) string {
	const max = 2000
	if len(s) > max {
		s = s[:max] + "\n…(clipped)\n"
	}
	if s != "" && !strings.HasSuffix(s, "\n") {
		s += "\n"
	}
	return s
}

func firstLine(s string) string {
	l, _, _ := strings.Cut(s, "\n")
	if len(l) > 80 {
		l = l[:80] + "…"
	}
	return l
}
