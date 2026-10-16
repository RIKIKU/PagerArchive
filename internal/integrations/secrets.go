package integrations

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

// A secrets blob is version (1 byte) | key ID (4 bytes) | nonce (12 bytes) |
// AES-256-GCM ciphertext and tag. The associated data is the integration's
// uid, so a blob copied onto another row doesn't decrypt (ADR 0006 §4).
const blobVersion = 1

var (
	// ErrNoKey: INTEGRATION_SECRETS_KEY isn't set, so integrations are off.
	ErrNoKey = errors.New("integrations are off: the server has no INTEGRATION_SECRETS_KEY")
	// ErrWrongKey: the blob was sealed with a key the server doesn't have.
	ErrWrongKey = errors.New("this integration's secrets were saved with another key: enter them again")
	// ErrCorrupt: the blob doesn't decrypt with the key it names.
	ErrCorrupt = errors.New("this integration's secrets can't be read: enter them again")
)

// Box seals and opens integration secrets. The key lives outside the
// database (INTEGRATION_SECRETS_KEY). Old, when set, is the previous key,
// kept only while rotating.
type Box struct {
	cur, old     cipher.AEAD
	curID, oldID [4]byte
}

// NewBox reads base64 keys of 32 bytes. old may be empty.
func NewBox(key, old string) (*Box, error) {
	b := &Box{}
	var err error
	if b.cur, b.curID, err = parseKey("INTEGRATION_SECRETS_KEY", key); err != nil {
		return nil, err
	}
	if old != "" {
		if b.old, b.oldID, err = parseKey("INTEGRATION_SECRETS_KEY_OLD", old); err != nil {
			return nil, err
		}
	}
	return b, nil
}

func parseKey(name, s string) (cipher.AEAD, [4]byte, error) {
	var id [4]byte
	s = strings.TrimSpace(s)
	var raw []byte
	var err error
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if raw, err = enc.DecodeString(s); err == nil {
			break
		}
	}
	if err != nil || len(raw) != 32 {
		return nil, id, fmt.Errorf("%s must be 32 random bytes in base64 (openssl rand -base64 32)", name)
	}
	block, err := aes.NewCipher(raw)
	if err != nil {
		return nil, id, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, id, err
	}
	sum := sha256.Sum256(append([]byte("pagerarchive integration key id\n"), raw...))
	copy(id[:], sum[:4])
	return aead, id, nil
}

// Seal encrypts plain for the integration uid with the current key.
func (b *Box) Seal(uid string, plain []byte) ([]byte, error) {
	if b == nil {
		return nil, ErrNoKey
	}
	nonce := make([]byte, b.cur.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	out := append([]byte{blobVersion}, b.curID[:]...)
	out = append(out, nonce...)
	return b.cur.Seal(out, nonce, plain, []byte(uid)), nil
}

// Open decrypts a blob sealed for uid, with the current or the old key.
func (b *Box) Open(uid string, blob []byte) ([]byte, error) {
	if b == nil {
		return nil, ErrNoKey
	}
	aead, err := b.keyFor(blob)
	if err != nil {
		return nil, err
	}
	n := aead.NonceSize()
	if len(blob) < 5+n {
		return nil, ErrCorrupt
	}
	plain, err := aead.Open(nil, blob[5:5+n], blob[5+n:], []byte(uid))
	if err != nil {
		return nil, ErrCorrupt
	}
	return plain, nil
}

// Stale reports whether a blob was sealed with the old key and should be
// sealed again with the current one.
func (b *Box) Stale(blob []byte) bool {
	return b != nil && b.old != nil && len(blob) >= 5 && blob[0] == blobVersion && [4]byte(blob[1:5]) == b.oldID
}

func (b *Box) keyFor(blob []byte) (cipher.AEAD, error) {
	if len(blob) < 5 || blob[0] != blobVersion {
		return nil, ErrCorrupt
	}
	id := [4]byte(blob[1:5])
	switch {
	case id == b.curID:
		return b.cur, nil
	case b.old != nil && id == b.oldID:
		return b.old, nil
	}
	return nil, ErrWrongKey
}
