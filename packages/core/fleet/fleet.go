// Package fleet keeps the user's servers: where they are, which host key is
// pinned and which user VPSGoGo logs in as. Private keys live in the vault,
// never in the database. See docs/SPEC.md §3.
package fleet

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/bubbleptr/vpsgogo/packages/core/vault"
)

// ErrNotFound is returned for a server ID that is not in the fleet.
var ErrNotFound = errors.New("fleet: server not found")

// Server is one VPS.
type Server struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Host      string    `json:"host"`
	Port      int       `json:"port"`
	User      string    `json:"user"` // root until provisioning switches to gogo
	HostKeyFP string    `json:"hostKeyFp"`
	Provider  string    `json:"provider"`
	CreatedAt time.Time `json:"createdAt"`
}

// NewServer is what the user enters to add a server.
type NewServer struct {
	Name     string `json:"name"`
	Host     string `json:"host"`
	Port     int    `json:"port"` // 0 means 22
	Provider string `json:"provider"`
}

func (n NewServer) normalize() (NewServer, error) {
	n.Host = strings.TrimSpace(n.Host)
	n.Name = strings.TrimSpace(n.Name)
	n.Provider = strings.TrimSpace(n.Provider)
	if n.Host == "" {
		return n, errors.New("fleet: host is required")
	}
	if net.ParseIP(n.Host) == nil && !isHostname(n.Host) {
		return n, fmt.Errorf("fleet: %q is not an IP address or host name", n.Host)
	}
	if n.Port == 0 {
		n.Port = 22
	}
	if n.Port < 1 || n.Port > 65535 {
		return n, fmt.Errorf("fleet: port %d is out of range", n.Port)
	}
	if n.Name == "" {
		n.Name = n.Host
	}
	return n, nil
}

func isHostname(h string) bool {
	if len(h) > 253 {
		return false
	}
	for _, label := range strings.Split(h, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, r := range label {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-') {
				return false
			}
		}
	}
	return true
}

// Repository stores servers.
type Repository interface {
	Insert(ctx context.Context, s Server) error
	Get(ctx context.Context, id string) (Server, error)
	List(ctx context.Context) ([]Server, error)
	Delete(ctx context.Context, id string) error
	UpdateUser(ctx context.Context, id, user string) error
}

// Service is the fleet's use cases.
type Service struct {
	repo  Repository
	vault vault.Vault
	now   func() time.Time
}

func NewService(repo Repository, v vault.Vault) *Service {
	return &Service{repo: repo, vault: v, now: time.Now}
}

// Add stores a server whose host key fingerprint the user has confirmed.
func (s *Service) Add(ctx context.Context, n NewServer, fingerprint string) (Server, error) {
	n, err := n.normalize()
	if err != nil {
		return Server{}, err
	}
	if fingerprint == "" {
		return Server{}, errors.New("fleet: the host key fingerprint must be confirmed first")
	}
	srv := Server{
		ID:        newID(),
		Name:      n.Name,
		Host:      n.Host,
		Port:      n.Port,
		User:      "root",
		HostKeyFP: fingerprint,
		Provider:  n.Provider,
		CreatedAt: s.now().UTC().Truncate(time.Millisecond),
	}
	if err := s.repo.Insert(ctx, srv); err != nil {
		return Server{}, err
	}
	return srv, nil
}

func (s *Service) Get(ctx context.Context, id string) (Server, error) { return s.repo.Get(ctx, id) }

func (s *Service) List(ctx context.Context) ([]Server, error) { return s.repo.List(ctx) }

// SetUser records the login user, which provisioning switches from root to gogo.
func (s *Service) SetUser(ctx context.Context, id, user string) error {
	return s.repo.UpdateUser(ctx, id, user)
}

// Delete removes the server and its private key.
func (s *Service) Delete(ctx context.Context, id string) error {
	if err := s.vault.Delete(privateKeyRef(id)); err != nil {
		return err
	}
	return s.repo.Delete(ctx, id)
}

// SetPrivateKey keeps the server's SSH private key in the vault.
func (s *Service) SetPrivateKey(id string, privatePEM []byte) error {
	return s.vault.Set(privateKeyRef(id), privatePEM)
}

// PrivateKey returns the server's SSH private key.
func (s *Service) PrivateKey(id string) ([]byte, error) { return s.vault.Get(privateKeyRef(id)) }

func privateKeyRef(id string) string { return "server/" + id + "/ssh-key" }

func newID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}
