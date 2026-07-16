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
	"sync"
	"sync/atomic"

	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/hkdf"
)

// authInfo domain-separates the handshake HMAC from key derivation.
const authInfo = "arnos-auth-v1"

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

	// Receive-side anti-replay. The transport (WebSocket over TLS) is ordered
	// and reliable, so an authenticated frame's counter must be strictly greater
	// than any previously accepted one. A frame that repeats or rewinds a
	// counter is a replay and is rejected. recvMu serializes the check so it is
	// safe even if Open is ever called concurrently.
	recvMu   sync.Mutex
	recvMax  uint64
	recvSeen bool
}

// DeriveSession builds the session keys from the PSK and both salts using
// HKDF-SHA256. c2sInfo/s2cInfo tie each key to a direction so the two streams
// never share a keystream. isServer selects which key the local side uses to
// send: the server sends server-to-client, the client sends client-to-server.
func DeriveSession(psk, clientSalt, serverSalt []byte, isServer bool) (*Session, error) {
	salt := make([]byte, 0, len(clientSalt)+len(serverSalt))
	salt = append(salt, clientSalt...)
	salt = append(salt, serverSalt...)

	c2s, err := expandKey(psk, salt, "arnos-c2s-v1")
	if err != nil {
		return nil, err
	}
	s2c, err := expandKey(psk, salt, "arnos-s2c-v1")
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

const (
	// padHeaderLen is the 2-byte big-endian real-payload length that prefixes
	// every sealed frame's plaintext, letting the peer strip the random pad.
	padHeaderLen = 2
	// MaxPad bounds the random padding added to each frame. Random padding makes
	// the sequence of ciphertext sizes unique and non-repeating per connection,
	// defeating length-based traffic fingerprinting, without meaningful overhead.
	MaxPad = 256
)

// randPad returns a random-length slice of random bytes, 0..MaxPad.
func randPad() []byte {
	var b [2]byte
	_, _ = rand.Read(b[:])
	n := int(uint16(b[0])<<8|uint16(b[1])) % (MaxPad + 1)
	if n == 0 {
		return nil
	}
	pad := make([]byte, n)
	_, _ = rand.Read(pad)
	return pad
}

// Seal encrypts one IP packet into a wire frame, adding random padding so the
// on-wire frame size never repeats.
func (s *Session) Seal(payload []byte) []byte {
	return s.sealRaw(payload, randPad())
}

// sealRaw is the deterministic core of Seal (given fixed pad). The plaintext is
// realLen(2) || payload || pad; the frame is counter(8) || AEAD(plaintext).
func (s *Session) sealRaw(payload, pad []byte) []byte {
	ctr := s.sendCtr.Add(1) - 1
	nonce := nonceFor(ctr)

	pt := make([]byte, padHeaderLen+len(payload)+len(pad))
	pt[0] = byte(len(payload) >> 8)
	pt[1] = byte(len(payload))
	copy(pt[padHeaderLen:], payload)
	copy(pt[padHeaderLen+len(payload):], pad)

	out := make([]byte, 8, 8+len(pt)+chacha20poly1305.Overhead)
	out[0] = byte(ctr >> 56)
	out[1] = byte(ctr >> 48)
	out[2] = byte(ctr >> 40)
	out[3] = byte(ctr >> 32)
	out[4] = byte(ctr >> 24)
	out[5] = byte(ctr >> 16)
	out[6] = byte(ctr >> 8)
	out[7] = byte(ctr)
	return s.send.Seal(out, nonce, pt, nil)
}

// ErrShortFrame is returned when a data frame is too small or malformed.
var ErrShortFrame = errors.New("arnos: short data frame")

// ErrReplay is returned when a frame reuses or rewinds the receive counter,
// i.e. it is a replay of a previously accepted frame.
var ErrReplay = errors.New("arnos: replayed frame")

// Open decrypts one data frame produced by Seal and strips the padding. It
// rejects replays: the counter is only advanced after the AEAD tag verifies, so
// a forged frame cannot poison the replay state, and any authenticated frame
// whose counter does not advance is dropped.
func (s *Session) Open(frame []byte) ([]byte, error) {
	if len(frame) < 8 {
		return nil, ErrShortFrame
	}
	ctr := uint64(frame[0])<<56 | uint64(frame[1])<<48 | uint64(frame[2])<<40 |
		uint64(frame[3])<<32 | uint64(frame[4])<<24 | uint64(frame[5])<<16 |
		uint64(frame[6])<<8 | uint64(frame[7])
	pt, err := s.recv.Open(nil, nonceFor(ctr), frame[8:], nil)
	if err != nil {
		return nil, err
	}
	// Anti-replay: only authenticated frames reach here. Require the counter to
	// strictly advance (the ordered transport guarantees legitimate frames do).
	s.recvMu.Lock()
	if s.recvSeen && ctr <= s.recvMax {
		s.recvMu.Unlock()
		return nil, ErrReplay
	}
	s.recvMax = ctr
	s.recvSeen = true
	s.recvMu.Unlock()

	if len(pt) < padHeaderLen {
		return nil, ErrShortFrame
	}
	realLen := int(pt[0])<<8 | int(pt[1])
	if realLen > len(pt)-padHeaderLen {
		return nil, ErrShortFrame
	}
	return pt[padHeaderLen : padHeaderLen+realLen], nil
}
