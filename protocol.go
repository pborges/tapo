package tapo

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

const (
	// DefaultPort is the TCP and UDP port used by legacy Kasa devices.
	DefaultPort = 9999
	initialKey  = byte(0xab)
	maxFrameLen = 1 << 20
)

// encrypt applies the TP-Link autokey XOR cipher. UDP packets contain this
// result directly; TCP packets add a four-byte big-endian length prefix.
func encrypt(plain []byte) []byte {
	out := make([]byte, len(plain))
	key := initialKey
	for i, b := range plain {
		out[i] = b ^ key
		key = out[i]
	}
	return out
}

func decrypt(ciphertext []byte) []byte {
	out := make([]byte, len(ciphertext))
	key := initialKey
	for i, b := range ciphertext {
		out[i] = b ^ key
		key = b
	}
	return out
}

func writeFrame(w io.Writer, plain []byte) error {
	if len(plain) > maxFrameLen {
		return fmt.Errorf("tapo: request is too large: %d bytes", len(plain))
	}
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(plain)))
	if err := writeAll(w, header[:]); err != nil {
		return err
	}
	return writeAll(w, encrypt(plain))
}

func writeAll(w io.Writer, data []byte) error {
	for len(data) > 0 {
		n, err := w.Write(data)
		if err != nil {
			return err
		}
		if n <= 0 || n > len(data) {
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	return nil
}

func readFrame(r io.Reader) ([]byte, error) {
	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(header[:])
	if n == 0 || n > maxFrameLen {
		return nil, fmt.Errorf("tapo: invalid response length %d", n)
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, err
	}
	plain := decrypt(buf)
	if len(plain) == 0 {
		return nil, errors.New("tapo: empty response")
	}
	return plain, nil
}
