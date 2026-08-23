package crypto

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"os"
)

func FromEnv() (*Encryptor, *BlindIndex, error) {
	key, err := hex.DecodeString(os.Getenv("ENCRYPTION_KEY"))
	if err != nil || len(key) < 32 {
		if os.Getenv("ENCRYPTION_KEY") == "" {
			key = make([]byte, 32)
			if _, rerr := rand.Read(key); rerr != nil {
				return nil, nil, rerr
			}
			log.Println("WARNING: ENCRYPTION_KEY not set; using an ephemeral development key. Set ENCRYPTION_KEY (64 hex chars) in production.")
		} else {
			return nil, nil, fmt.Errorf("ENCRYPTION_KEY must be a 64-hex-char (32-byte) value")
		}
	}
	pepper, err := hex.DecodeString(os.Getenv("BLIND_INDEX_KEY"))
	if err != nil || len(pepper) < 32 {
		if os.Getenv("BLIND_INDEX_KEY") == "" {
			pepper = make([]byte, 32)
			if _, rerr := rand.Read(pepper); rerr != nil {
				return nil, nil, rerr
			}
			log.Println("WARNING: BLIND_INDEX_KEY not set; using an ephemeral development pepper.")
		} else {
			return nil, nil, errors.New("BLIND_INDEX_KEY must be a 64-hex-char (32-byte) value")
		}
	}
	enc, err := NewEncryptor([]KeyMaterial{{Version: 1, Key: key[:32]}}, 1)
	if err != nil {
		return nil, nil, err
	}
	bi, err := NewBlindIndex(pepper)
	if err != nil {
		return nil, nil, err
	}
	return enc, bi, nil
}
