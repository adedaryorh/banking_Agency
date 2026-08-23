package crypto

import (
	"encoding/hex"
	"fmt"
)

// AES256 is a simplified wrapper around Encryptor for beneficiary encryption
type AES256 struct {
	enc *Encryptor
}

// NewAES256Encryptor creates a new AES-256 encryptor with a single key
func NewAES256Encryptor(key []byte) *AES256 {
	if len(key) != 32 {
		panic(fmt.Sprintf("crypto: key must be 32 bytes, got %d", len(key)))
	}
	enc, err := NewEncryptor([]KeyMaterial{{Version: 1, Key: key}}, 1)
	if err != nil {
		panic(fmt.Sprintf("crypto: failed to create encryptor: %v", err))
	}
	return &AES256{enc: enc}
}

// NewAES256Decryptor creates a new AES-256 decryptor (same as encryptor for symmetric crypto)
func NewAES256Decryptor(key []byte) *AES256 {
	return NewAES256Encryptor(key)
}

// Encrypt encrypts plaintext and returns hex-encoded ciphertext
func (a *AES256) Encrypt(plaintext string) (string, error) {
	if plaintext == "" {
		return "", nil
	}
	ciphertext, _, err := a.enc.Encrypt([]byte(plaintext), nil)
	if err != nil {
		return "", fmt.Errorf("encrypt: %w", err)
	}
	return hex.EncodeToString(ciphertext), nil
}

// Decrypt decrypts hex-encoded ciphertext and returns plaintext
func (a *AES256) Decrypt(ciphertextHex string) (string, error) {
	if ciphertextHex == "" {
		return "", nil
	}
	ciphertext, err := hex.DecodeString(ciphertextHex)
	if err != nil {
		return "", fmt.Errorf("decrypt: invalid hex: %w", err)
	}
	plaintext, err := a.enc.Decrypt(ciphertext, nil)
	if err != nil {
		return "", fmt.Errorf("decrypt: %w", err)
	}
	return string(plaintext), nil
}
