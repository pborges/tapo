package tapo

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"testing"
)

type shortWriter struct{ bytes.Buffer }

func (w *shortWriter) Write(data []byte) (int, error) {
	if len(data) > 2 {
		data = data[:2]
	}
	return w.Buffer.Write(data)
}

func TestCipherKnownPrefixAndRoundTrip(t *testing.T) {
	t.Parallel()
	plain := []byte(`{"system":{"get_sysinfo":null}}`)
	ciphertext := encrypt(plain)
	wantPrefix := []byte{0xd0, 0xf2, 0x81}
	if !bytes.Equal(ciphertext[:len(wantPrefix)], wantPrefix) {
		t.Fatalf("cipher prefix = %x, want %x", ciphertext[:len(wantPrefix)], wantPrefix)
	}
	if got := decrypt(ciphertext); !bytes.Equal(got, plain) {
		t.Fatalf("decrypt(encrypt(%q)) = %q", plain, got)
	}
}

func TestWriteFrameHandlesShortWrites(t *testing.T) {
	t.Parallel()
	plain := []byte(`{"system":{"get_sysinfo":null}}`)
	var framed shortWriter
	if err := writeFrame(&framed, plain); err != nil {
		t.Fatal(err)
	}
	got, err := readFrame(bytes.NewReader(framed.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatalf("readFrame = %q, want %q", got, plain)
	}
}

type zeroWriter struct{}

func (zeroWriter) Write([]byte) (int, error) { return 0, nil }

func TestWriteFrameRejectsNoProgress(t *testing.T) {
	t.Parallel()
	if err := writeFrame(zeroWriter{}, []byte("x")); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("writeFrame error = %v, want io.ErrShortWrite", err)
	}
}

func TestFrameRoundTrip(t *testing.T) {
	t.Parallel()
	plain := []byte(`{"emeter":{"get_realtime":{}}}`)
	var framed bytes.Buffer
	if err := writeFrame(&framed, plain); err != nil {
		t.Fatal(err)
	}
	if got := binary.BigEndian.Uint32(framed.Bytes()[:4]); got != uint32(len(plain)) {
		t.Fatalf("length = %d, want %d", got, len(plain))
	}
	got, err := readFrame(&framed)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatalf("readFrame = %q, want %q", got, plain)
	}
}

func TestReadFrameRejectsOversizedResponse(t *testing.T) {
	t.Parallel()
	var framed bytes.Buffer
	if err := binary.Write(&framed, binary.BigEndian, uint32(maxFrameLen+1)); err != nil {
		t.Fatal(err)
	}
	if _, err := readFrame(&framed); err == nil {
		t.Fatal("readFrame accepted an oversized response")
	}
}
