package helpers

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
)

var (
	ErrInvalidKey      = errors.New("encryption key must be 32 bytes")
	ErrDecryptFailed   = errors.New("decryption failed")
	ErrCiphertextShort = errors.New("ciphertext is too short")
)

type FieldCipher struct {
	aead    cipher.AEAD
	hashKey []byte
}

func NewFieldCipher(hexKey string) (*FieldCipher, error) {
	key, err := hex.DecodeString(hexKey)
	if err != nil {
		return nil, fmt.Errorf("%w: not valid hex", ErrInvalidKey)
	}
	if len(key) != 32 {
		return nil, ErrInvalidKey
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("build cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("build gcm: %w", err)
	}

	mac := hmac.New(sha256.New, key)
	mac.Write([]byte("nabla/field-index/v1"))

	return &FieldCipher{aead: aead, hashKey: mac.Sum(nil)}, nil
}

func (c *FieldCipher) Encrypt(plaintext string) (string, error) {
	if c == nil {
		return "", ErrInvalidKey
	}
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("generate nonce: %w", err)
	}
	sealed := c.aead.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(sealed), nil
}

func (c *FieldCipher) Decrypt(encoded string) (string, error) {
	if c == nil {
		return "", ErrInvalidKey
	}
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("%w: not valid base64", ErrDecryptFailed)
	}
	nonceSize := c.aead.NonceSize()
	if len(raw) < nonceSize {
		return "", ErrCiphertextShort
	}
	plaintext, err := c.aead.Open(nil, raw[:nonceSize], raw[nonceSize:], nil)
	if err != nil {
		return "", ErrDecryptFailed
	}
	return string(plaintext), nil
}

func (c *FieldCipher) IndexHash(value string) string {
	if c == nil {
		return ""
	}
	mac := hmac.New(sha256.New, c.hashKey)
	mac.Write([]byte(value))
	return hex.EncodeToString(mac.Sum(nil))
}

func HashToken(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func ConstantTimeEquals(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func GenerateNumericCode(length int) (string, error) {
	if length <= 0 {
		return "", fmt.Errorf("code length must be positive")
	}
	digits := make([]byte, length)
	buf := make([]byte, length)
	if _, err := io.ReadFull(rand.Reader, buf); err != nil {
		return "", fmt.Errorf("generate code: %w", err)
	}
	for i, b := range buf {
		digits[i] = '0' + (b % 10)
	}
	return string(digits), nil
}

func GenerateOpaqueToken(byteLength int) (string, error) {
	if byteLength <= 0 {
		byteLength = 32
	}
	buf := make([]byte, byteLength)
	if _, err := io.ReadFull(rand.Reader, buf); err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func MaskTail(value string, n int) string {
	if len(value) <= n {
		return value
	}
	masked := make([]byte, len(value)-n)
	for i := range masked {
		masked[i] = '*'
	}
	return string(masked) + value[len(value)-n:]
}
