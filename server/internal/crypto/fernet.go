package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"time"
)

// Fernet implements the Fernet spec (github.com/fernet/spec), which Python's
// cryptography.fernet wrote users.jellyfin_token_enc with:
//
//	token = base64url( 0x80 | timestamp(8, big endian) | IV(16) | AES-128-CBC ciphertext | HMAC-SHA256(32) )
//
// The 32-byte key splits into a 16-byte signing key and a 16-byte
// encryption key. Written here (about 60 lines) rather than taken from a
// third-party module: the format is small and fixed, and a dependency for it
// would be one more thing to audit.
type Fernet struct {
	signKey [16]byte
	encKey  [16]byte
}

// ErrFernet is returned for any token that is not a valid Fernet token under
// this key (bad base64, wrong version, wrong MAC, bad padding). One error for
// all of them, on purpose: telling them apart would help an attacker.
var ErrFernet = errors.New("not a valid encrypted value for this SECRET_KEY")

const fernetVersion = 0x80

func newFernet(key []byte) *Fernet {
	f := &Fernet{}
	copy(f.signKey[:], key[:16])
	copy(f.encKey[:], key[16:32])
	return f
}

// Encrypt seals plain into a Fernet token.
func (f *Fernet) Encrypt(plain string) string {
	iv := make([]byte, aes.BlockSize)
	_, _ = rand.Read(iv)
	return f.encryptWith(plain, iv, time.Now())
}

func (f *Fernet) encryptWith(plain string, iv []byte, now time.Time) string {
	block, _ := aes.NewCipher(f.encKey[:]) // a 16-byte key never errors
	padded := pkcs7Pad([]byte(plain), aes.BlockSize)
	ct := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(ct, padded)

	msg := make([]byte, 0, 1+8+len(iv)+len(ct)+sha256.Size)
	msg = append(msg, fernetVersion)
	msg = binary.BigEndian.AppendUint64(msg, uint64(now.Unix()))
	msg = append(msg, iv...)
	msg = append(msg, ct...)
	mac := hmac.New(sha256.New, f.signKey[:])
	mac.Write(msg)
	msg = mac.Sum(msg)
	return base64.URLEncoding.EncodeToString(msg)
}

// Decrypt opens a Fernet token. There is no age limit (the Python never set
// a ttl: a Jellyfin token stays valid until the person unlinks it).
func (f *Fernet) Decrypt(token string) (string, error) {
	raw, err := base64.URLEncoding.DecodeString(token)
	if err != nil {
		return "", ErrFernet
	}
	// version + timestamp + IV + at least one block + MAC
	if len(raw) < 1+8+16+16+sha256.Size || raw[0] != fernetVersion {
		return "", ErrFernet
	}
	body, sum := raw[:len(raw)-sha256.Size], raw[len(raw)-sha256.Size:]
	mac := hmac.New(sha256.New, f.signKey[:])
	mac.Write(body)
	if !hmac.Equal(mac.Sum(nil), sum) {
		return "", ErrFernet
	}
	iv, ct := body[9:25], body[25:]
	if len(ct)%aes.BlockSize != 0 {
		return "", ErrFernet
	}
	block, _ := aes.NewCipher(f.encKey[:])
	plain := make([]byte, len(ct))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(plain, ct)
	out, ok := pkcs7Unpad(plain, aes.BlockSize)
	if !ok {
		return "", ErrFernet
	}
	return string(out), nil
}

func pkcs7Pad(b []byte, size int) []byte {
	n := size - len(b)%size
	out := make([]byte, len(b), len(b)+n)
	copy(out, b)
	for i := 0; i < n; i++ {
		out = append(out, byte(n))
	}
	return out
}

func pkcs7Unpad(b []byte, size int) ([]byte, bool) {
	if len(b) == 0 || len(b)%size != 0 {
		return nil, false
	}
	n := int(b[len(b)-1])
	if n == 0 || n > size {
		return nil, false
	}
	for _, c := range b[len(b)-n:] {
		if int(c) != n {
			return nil, false
		}
	}
	return b[:len(b)-n], true
}
