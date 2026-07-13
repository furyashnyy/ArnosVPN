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
	vecAuthB64  = "zLJxYyyzQhwNFXcTqynuTczB1YssAli3ynQBI2vBLd4="
	vecFrameHex = "0000000000000000674d301ce11e2596a02e38cd5d46102ffb75ed5a2129515e346affdc6513f78fade3a103"
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
	return psk, cs, ss, 1700000000, []byte("arno-vpn-test-vector")
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
