package protocol

import (
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"strconv"
	"sync/atomic"

	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/hkdf"
)

// authInfo domain-separates the handshake HMAC from key derivation.
const authInfo = "arno-auth-v1"

// RandBytes returns n cryptographically-random bytes.
func RandBytes(n int) ([]byte, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	return b, nil
}

// authContext is the message authenticated by the Hello HMAC. Both sides must
// build it identically: the literal authInfo, the client salt, and the
// decimal ASCII timestamp.
func authContext(salt []byte, ts int64) []byte {
	buf := make([]byte, 0, len(authInfo)+len(salt)+20)
	buf = append(buf, authInfo...)
	buf = append(buf, salt...)
	buf = append(buf, []byte(strconv.FormatInt(ts, 10))...)
	return buf
}

// ComputeAuth returns the base64 HMAC-SHA256 proving knowledge of the PSK.
func ComputeAuth(psk, salt []byte, ts int64) string {
	m := hmac.New(sha256.New, psk)
	m.Write(authContext(salt, ts))
	return base64.StdEncoding.EncodeToString(m.Sum(nil))
}

// VerifyAuth checks a Hello's auth token in constant time.
func VerifyAuth(psk, salt []byte, ts int64, auth string) bool {
	want := ComputeAuth(psk, salt, ts)
	return subtle.ConstantTimeCompare([]byte(want), []byte(auth)) == 1
}

// Session holds the per-connection AEAD state for both directions. Each
// direction has its own key and monotonically increasing nonce counter.
type Session struct {
	send    cipher.AEAD
	recv    cipher.AEAD
	sendCtr atomic.Uint64
	recvCtr atomic.Uint64
}

// DeriveSession builds the session keys from the PSK and both salts using
// HKDF-SHA256. c2sInfo/s2cInfo tie each key to a direction so the two streams
// never share a keystream. isServer selects which key the local side uses to
// send: the server sends server-to-client, the client sends client-to-server.
func DeriveSession(psk, clientSalt, serverSalt []byte, isServer bool) (*Session, error) {
	salt := make([]byte, 0, len(clientSalt)+len(serverSalt))
	salt = append(salt, clientSalt...)
	salt = append(salt, serverSalt...)

	c2s, err := expandKey(psk, salt, "arno-c2s-v1")
	if err != nil {
		return nil, err
	}
	s2c, err := expandKey(psk, salt, "arno-s2c-v1")
	if err != nil {
		return nil, err
	}

	aeadC2S, err := chacha20poly1305.New(c2s)
	if err != nil {
		return nil, err
	}
	aeadS2C, err := chacha20poly1305.New(s2c)
	if err != nil {
		return nil, err
	}

	s := &Session{}
	if isServer {
		s.send, s.recv = aeadS2C, aeadC2S
	} else {
		s.send, s.recv = aeadC2S, aeadS2C
	}
	return s, nil
}

func expandKey(psk, salt []byte, info string) ([]byte, error) {
	r := hkdf.New(sha256.New, psk, salt, []byte(info))
	key := make([]byte, KeyLen)
	if _, err := r.Read(key); err != nil {
		return nil, err
	}
	return key, nil
}

// nonceFor writes a 12-byte nonce for the given counter: 4 zero bytes followed
// by the big-endian counter. Directions use distinct keys, so per-direction
// counters starting at zero never collide.
func nonceFor(ctr uint64) []byte {
	n := make([]byte, chacha20poly1305.NonceSize)
	n[4] = byte(ctr >> 56)
	n[5] = byte(ctr >> 48)
	n[6] = byte(ctr >> 40)
	n[7] = byte(ctr >> 32)
	n[8] = byte(ctr >> 24)
	n[9] = byte(ctr >> 16)
	n[10] = byte(ctr >> 8)
	n[11] = byte(ctr)
	return n
}

// Seal encrypts one plaintext IP packet. The 8-byte big-endian counter is
// prepended to the ciphertext so the peer can reconstruct the nonce without
// assuming in-order delivery.
func (s *Session) Seal(plaintext []byte) []byte {
	ctr := s.sendCtr.Add(1) - 1
	nonce := nonceFor(ctr)
	out := make([]byte, 8, 8+len(plaintext)+chacha20poly1305.Overhead)
	out[0] = byte(ctr >> 56)
	out[1] = byte(ctr >> 48)
	out[2] = byte(ctr >> 40)
	out[3] = byte(ctr >> 32)
	out[4] = byte(ctr >> 24)
	out[5] = byte(ctr >> 16)
	out[6] = byte(ctr >> 8)
	out[7] = byte(ctr)
	return s.send.Seal(out, nonce, plaintext, nil)
}

// ErrShortFrame is returned when a data frame is too small to contain a counter.
var ErrShortFrame = errors.New("arno: short data frame")

// Open decrypts one data frame produced by Seal.
func (s *Session) Open(frame []byte) ([]byte, error) {
	if len(frame) < 8 {
		return nil, ErrShortFrame
	}
	ctr := uint64(frame[0])<<56 | uint64(frame[1])<<48 | uint64(frame[2])<<40 |
		uint64(frame[3])<<32 | uint64(frame[4])<<24 | uint64(frame[5])<<16 |
		uint64(frame[6])<<8 | uint64(frame[7])
	s.recvCtr.Store(ctr)
	nonce := nonceFor(ctr)
	return s.recv.Open(nil, nonce, frame[8:], nil)
}
