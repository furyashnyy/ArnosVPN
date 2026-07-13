package protocol

import (
	"encoding/hex"
	"testing"
)

// These vectors pin the exact wire bytes for a fixed set of inputs. The Kotlin
// client has an identical test (apps/android .../CryptoTest.kt) asserting the
// same constants, so the two independent implementations are guaranteed to
// interoperate. If you change the crypto, regenerate both.
const (
	vecAuthB64  = "XcfGTzUpsovj7VPGo5bqQcC05d9BTevquB8kEtkiZQI="
	vecFrameHex = "0000000000000000d0867cadb3d67b40e6d82fb9bd2f190aba7ddc0a602a72c0ec9f6466003b20b0ae090cb6cd4ca9ef939073be"
)

func vecInputs() (psk, cs, ss []byte, ts int64, pt []byte) {
	psk = make([]byte, 32)
	for i := range psk {
		psk[i] = byte(i)
	}
	cs = make([]byte, 16)
	for i := range cs {
		cs[i] = byte(0xa0 + i)
	}
	ss = make([]byte, 16)
	for i := range ss {
		ss[i] = byte(0xb0 + i)
	}
	return psk, cs, ss, 1700000000, []byte("arnos-vpn-test-vector")
}

func TestVectorAuth(t *testing.T) {
	psk, cs, _, ts, _ := vecInputs()
	if got := ComputeAuth(psk, cs, ts); got != vecAuthB64 {
		t.Fatalf("auth vector drift:\n got %s\nwant %s", got, vecAuthB64)
	}
}

func TestVectorFrame(t *testing.T) {
	psk, cs, ss, _, pt := vecInputs()
	cli, err := DeriveSession(psk, cs, ss, false)
	if err != nil {
		t.Fatal(err)
	}
	// Deterministic pad (5 zero bytes) so the vector is stable; the real Seal
	// uses random padding for the fingerprint.
	frame := cli.sealRaw(pt, make([]byte, 5)) // first packet, counter 0
	if got := hex.EncodeToString(frame); got != vecFrameHex {
		t.Fatalf("frame vector drift:\n got %s\nwant %s", got, vecFrameHex)
	}
}
