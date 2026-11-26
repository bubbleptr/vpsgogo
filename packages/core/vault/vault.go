// Package vault stores secrets (SSH private keys, API keys, channel tokens)
// outside the database. The desktop app backs it with the system keychain;
// tests use Memory.
package vault

import (
	"errors"
	"sync"
)

// ErrNotFound is returned by Get for a key that holds no secret.
var ErrNotFound = errors.New("vault: secret not found")

// Vault keeps small secrets under string keys.
type Vault interface {
	Set(key string, value []byte) error
	Get(key string) ([]byte, error)
	Delete(key string) error
}

// Memory is an in-process Vault for tests.
type Memory struct {
	mu sync.Mutex
	m  map[string][]byte
}

func NewMemory() *Memory { return &Memory{m: map[string][]byte{}} }

func (v *Memory) Set(key string, value []byte) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.m[key] = append([]byte(nil), value...)
	return nil
}

func (v *Memory) Get(key string) ([]byte, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	b, ok := v.m[key]
	if !ok {
		return nil, ErrNotFound
	}
	return append([]byte(nil), b...), nil
}

func (v *Memory) Delete(key string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	delete(v.m, key)
	return nil
}
