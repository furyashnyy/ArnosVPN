package protocol

import (
	"bytes"
	"testing"
	"time"
)

func TestAuthRoundTrip(t *testing.T) {
	psk, _ := RandBytes(PSKLen)
	salt, _ := RandBytes(SaltLen)
	ts := time.Now().Unix()

	auth := ComputeAuth(psk, salt, ts)
	if !VerifyAuth(psk, salt, ts, auth) {
		t.Fatal("valid auth rejected")
	}
	// Wrong PSK must fail.
	other, _ := RandBytes(PSKLen)
	if VerifyAuth(other, salt, ts, auth) {
		t.Fatal("auth accepted with wrong PSK")
	}
	// Tampered timestamp must fail.
	if VerifyAuth(psk, salt, ts+1, auth) {
		t.Fatal("auth accepted with wrong timestamp")
	}
}

func TestSessionRoundTrip(t *testing.T) {
	psk, _ := RandBytes(PSKLen)
	clientSalt, _ := RandBytes(SaltLen)
	serverSalt, _ := RandBytes(SaltLen)

	srv, err := DeriveSession(psk, clientSalt, serverSalt, true)
	if err != nil {
		t.Fatal(err)
	}
	cli, err := DeriveSession(psk, clientSalt, serverSalt, false)
	if err != nil {
		t.Fatal(err)
	}

	// Client -> server.
	msg := []byte("the quick brown fox jumps over the lazy dog")
	frame := cli.Seal(msg)
	got, err := srv.Open(frame)
	if err != nil {
		t.Fatalf("server open: %v", err)
	}
	if !bytes.Equal(got, msg) {
		t.Fatalf("c2s mismatch: %q != %q", got, msg)
	}

	// Server -> client, several packets to exercise the counter.
	for i := 0; i < 5; i++ {
		p := bytes.Repeat([]byte{byte(i)}, 100+i)
		f := srv.Seal(p)
		out, err := cli.Open(f)
		if err != nil {
			t.Fatalf("client open %d: %v", i, err)
		}
		if !bytes.Equal(out, p) {
			t.Fatalf("s2c mismatch at %d", i)
		}
	}
}

func TestSessionRejectsTampered(t *testing.T) {
	psk, _ := RandBytes(PSKLen)
	cs, _ := RandBytes(SaltLen)
	ss, _ := RandBytes(SaltLen)
	srv, _ := DeriveSession(psk, cs, ss, true)
	cli, _ := DeriveSession(psk, cs, ss, false)

	frame := cli.Seal([]byte("hello"))
	frame[len(frame)-1] ^= 0xff // flip a ciphertext bit
	if _, err := srv.Open(frame); err == nil {
		t.Fatal("tampered frame accepted")
	}
}

func TestSessionRejectsReplay(t *testing.T) {
	psk, _ := RandBytes(PSKLen)
	cs, _ := RandBytes(SaltLen)
	ss, _ := RandBytes(SaltLen)
	srv, _ := DeriveSession(psk, cs, ss, true)
	cli, _ := DeriveSession(psk, cs, ss, false)

	f0 := cli.Seal([]byte("first"))
	f1 := cli.Seal([]byte("second"))

	if _, err := srv.Open(f0); err != nil {
		t.Fatalf("open f0: %v", err)
	}
	if _, err := srv.Open(f1); err != nil {
		t.Fatalf("open f1: %v", err)
	}
	// Replaying an already-accepted frame must be rejected.
	if _, err := srv.Open(f0); err != ErrReplay {
		t.Fatalf("replay of f0: got %v, want ErrReplay", err)
	}
	if _, err := srv.Open(f1); err != ErrReplay {
		t.Fatalf("replay of f1: got %v, want ErrReplay", err)
	}
}

func TestKeysAreDirectional(t *testing.T) {
	// A session must not be able to open its own sealed frame; send and recv
	// keys differ by direction.
	psk, _ := RandBytes(PSKLen)
	cs, _ := RandBytes(SaltLen)
	ss, _ := RandBytes(SaltLen)
	cli, _ := DeriveSession(psk, cs, ss, false)
	frame := cli.Seal([]byte("loopback should fail"))
	if _, err := cli.Open(frame); err == nil {
		t.Fatal("client opened its own frame; keys are not directional")
	}
}
