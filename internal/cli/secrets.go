package cli

import (
	"errors"
	"fmt"

	keyring "github.com/zalando/go-keyring"
)

const keyringService = "com.wuzupdog.updog-cli"

type secretStore interface {
	Get(id string) (string, error)
	Set(id, secret string) error
	Delete(id string) error
}

type osKeyring struct{}

func (osKeyring) Get(id string) (string, error) {
	secret, err := keyring.Get(keyringService, id)
	if errors.Is(err, keyring.ErrNotFound) {
		return "", errSecretNotFound
	}
	if err != nil {
		return "", fmt.Errorf("read credential from OS keyring: %w", err)
	}
	return secret, nil
}

func (osKeyring) Set(id, secret string) error {
	if err := keyring.Set(keyringService, id, secret); err != nil {
		return fmt.Errorf("save credential in OS keyring: %w", err)
	}
	return nil
}

func (osKeyring) Delete(id string) error {
	err := keyring.Delete(keyringService, id)
	if errors.Is(err, keyring.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("delete credential from OS keyring: %w", err)
	}
	return nil
}

var errSecretNotFound = errors.New("credential not found")
