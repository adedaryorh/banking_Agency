package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
)

var (
	ErrNoSuchKeyVersion = errors.New("crypto: no such key version")
	ErrCiphertextShort  = errors.New("crypto: ciphertext too short")
	ErrDecryptFailed    = errors.New("crypto: decryption failed")
)

type Encryptor struct {
	keys       map[uint16]cipher.AEAD
	currentVer uint16
}

// KeyMaterial is one versioned 32-byte encryption key.
type KeyMaterial struct {
	Version uint16
	Key     []byte
}

// NewEncryptor builds an encryptor. current must be present in keys.
func NewEncryptor(keys []KeyMaterial, current uint16) (*Encryptor, error) {
	if len(keys) == 0 {
		return nil, errors.New("crypto: at least one encryption key is required")
	}

	e := &Encryptor{keys: make(map[uint16]cipher.AEAD, len(keys)), currentVer: current}
	for _, k := range keys {
		if len(k.Key) != 32 {
			return nil, fmt.Errorf("crypto: key version %d must be 32 bytes, got %d", k.Version, len(k.Key))
		}
		block, err := aes.NewCipher(k.Key)
		if err != nil {
			return nil, fmt.Errorf("crypto: key version %d: %w", k.Version, err)
		}
		aead, err := cipher.NewGCM(block)
		if err != nil {
			return nil, fmt.Errorf("crypto: key version %d: %w", k.Version, err)
		}
		e.keys[k.Version] = aead
	}

	if _, ok := e.keys[current]; !ok {
		return nil, fmt.Errorf("%w: current version %d", ErrNoSuchKeyVersion, current)
	}
	return e, nil
}

// Encrypt seals plaintext with the current key.
func (e *Encryptor) Encrypt(plaintext, associatedData []byte) ([]byte, uint16, error) {
	aead := e.keys[e.currentVer]

	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, 0, fmt.Errorf("crypto: read nonce: %w", err)
	}

	out := make([]byte, 2, 2+len(nonce)+len(plaintext)+aead.Overhead())
	binary.BigEndian.PutUint16(out[0:2], e.currentVer)
	out = append(out, nonce...)
	out = aead.Seal(out, nonce, plaintext, associatedData)

	return out, e.currentVer, nil
}

// Decrypt opens a ciphertext produced by Encrypt
func (e *Encryptor) Decrypt(ciphertext, associatedData []byte) ([]byte, error) {
	if len(ciphertext) < 2 {
		return nil, ErrCiphertextShort
	}

	version := binary.BigEndian.Uint16(ciphertext[0:2])
	aead, ok := e.keys[version]
	if !ok {
		return nil, fmt.Errorf("%w: %d", ErrNoSuchKeyVersion, version)
	}

	nonceSize := aead.NonceSize()
	if len(ciphertext) < 2+nonceSize {
		return nil, ErrCiphertextShort
	}

	nonce := ciphertext[2 : 2+nonceSize]
	plaintext, err := aead.Open(nil, nonce, ciphertext[2+nonceSize:], associatedData)
	if err != nil {
		return nil, ErrDecryptFailed
	}
	return plaintext, nil
}

// CurrentVersion is the key version new writes will use
func (e *Encryptor) CurrentVersion() uint16 { return e.currentVer }

// NeedsRewrap reports whether a stored ciphertext was written with an older key
func (e *Encryptor) NeedsRewrap(ciphertext []byte) bool {
	if len(ciphertext) < 2 {
		return false
	}
	return binary.BigEndian.Uint16(ciphertext[0:2]) != e.currentVer
}

// AAD builds associated data from a table, column and row identifier
func AAD(table, column, rowID string) []byte {
	return []byte(table + ":" + column + ":" + rowID)
}

type BlindIndex struct {
	pepper []byte
}

// NewBlindIndex builds a blind index generator
func NewBlindIndex(pepper []byte) (*BlindIndex, error) {
	if len(pepper) < 32 {
		return nil, errors.New("crypto: blind index pepper must be at least 32 bytes")
	}
	return &BlindIndex{pepper: pepper}, nil
}

// Compute normalises the value (trim, casefold) and returns its HMAC
func (b *BlindIndex) Compute(domain, value string) []byte {
	normalised := strings.ToLower(strings.TrimSpace(value))
	mac := hmac.New(sha256.New, b.pepper)
	mac.Write([]byte(domain))
	mac.Write([]byte{0x00})
	mac.Write([]byte(normalised))
	return mac.Sum(nil)
}
