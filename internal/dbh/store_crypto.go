package dbh

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const storeKeyFile = ".store.key"

type encryptedEnvelope struct {
	Version    int    `json:"version"`
	Encrypted  bool   `json:"encrypted"`
	Nonce      string `json:"nonce"`
	Ciphertext string `json:"ciphertext"`
}

func resolveStoreKey(dir string, create bool) ([]byte, error) {
	if raw := os.Getenv("DBH_STORE_KEY"); raw != "" {
		sum := sha256.Sum256([]byte(raw))
		return sum[:], nil
	}
	keyPath := filepath.Join(dir, storeKeyFile)
	data, err := os.ReadFile(keyPath)
	if err == nil {
		decoded, decErr := base64.StdEncoding.DecodeString(strings.TrimSpace(string(data)))
		if decErr != nil || len(decoded) != 32 {
			return nil, fmt.Errorf("invalid store key in %s", keyPath)
		}
		return decoded, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if !create {
		return nil, os.ErrNotExist
	}
	key := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return nil, err
	}
	encoded := base64.StdEncoding.EncodeToString(key) + "\n"
	if err := writeAtomicFileMode(keyPath, []byte(encoded), 0600); err != nil {
		return nil, err
	}
	return key, nil
}

func encryptConnections(dir string, plaintext []byte) ([]byte, error) {
	key, err := resolveStoreKey(dir, true)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	ct := gcm.Seal(nil, nonce, plaintext, nil)
	env := encryptedEnvelope{
		Version:    1,
		Encrypted:  true,
		Nonce:      base64.StdEncoding.EncodeToString(nonce),
		Ciphertext: base64.StdEncoding.EncodeToString(ct),
	}
	return json.MarshalIndent(env, "", "  ")
}

func decryptConnections(dir string, data []byte) ([]byte, bool, error) {
	var env encryptedEnvelope
	if err := json.Unmarshal(data, &env); err == nil && env.Encrypted && env.Ciphertext != "" {
		key, err := resolveStoreKey(dir, false)
		if err != nil {
			return nil, true, fmt.Errorf("cannot load encryption key for connections.json: %w", err)
		}
		nonce, err := base64.StdEncoding.DecodeString(env.Nonce)
		if err != nil {
			return nil, true, fmt.Errorf("invalid connections.json nonce: %w", err)
		}
		ct, err := base64.StdEncoding.DecodeString(env.Ciphertext)
		if err != nil {
			return nil, true, fmt.Errorf("invalid connections.json ciphertext: %w", err)
		}
		block, err := aes.NewCipher(key)
		if err != nil {
			return nil, true, err
		}
		gcm, err := cipher.NewGCM(block)
		if err != nil {
			return nil, true, err
		}
		plain, err := gcm.Open(nil, nonce, ct, nil)
		if err != nil {
			return nil, true, fmt.Errorf("cannot decrypt connections.json: %w", err)
		}
		return plain, true, nil
	}
	// Legacy plaintext connections.json
	return data, false, nil
}
