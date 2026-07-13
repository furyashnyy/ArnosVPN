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
	vecFrameHex = "0000000000000000b1e173b0ae947e1dfe8535f1ba3e4708f268cd06661a266cf495e88ef6890555a6a22ccc04"
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
	frame := cli.Seal(pt) // first packet, counter 0
	if got := hex.EncodeToString(frame); got != vecFrameHex {
		t.Fatalf("frame vector drift:\n got %s\nwant %s", got, vecFrameHex)
	}
}
