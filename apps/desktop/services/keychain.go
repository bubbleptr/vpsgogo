package services

import (
	"errors"

	"github.com/bubbleptr/vpsgogo/packages/core/vault"
	"github.com/egoist/mygo"
)

// Keychain is the vault.Vault of the desktop app: the system credential store.
type Keychain struct{}

var _ vault.Vault = Keychain{}

func (Keychain) Set(key string, value []byte) error { return mygo.Secrets.Set(key, value) }

func (Keychain) Get(key string) ([]byte, error) {
	b, err := mygo.Secrets.Get(key)
	if errors.Is(err, mygo.ErrSecretNotFound) {
		return nil, vault.ErrNotFound
	}
	return b, err
}

func (Keychain) Delete(key string) error {
	err := mygo.Secrets.Delete(key)
	if errors.Is(err, mygo.ErrSecretNotFound) {
		return nil
	}
	return err
}
