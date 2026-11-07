// Package backup provides encrypted configuration backup containers.
package backup

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"io"
	"math"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/chacha20poly1305"
)

var (
	ErrWrongPassword      = errors.New("wrong password")
	ErrUnsupportedVersion = errors.New("unsupported version")
	ErrInvalidArchive     = errors.New("invalid archive")
)

type Header struct {
	Version   uint16
	Time      uint32
	MemoryKiB uint32
	Threads   uint8
	Salt      [16]byte
}

const (
	frameHeaderSize = 51
	frameChunkSize  = 64 * 1024
)

// frameTestParams is nil in production; tests can override the key derivation cost.
var frameTestParams *Header

func frameReadFull(src io.Reader, buf []byte) error {
	_, err := io.ReadFull(src, buf)
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return ErrInvalidArchive
	}
	return err
}

func frameWrite(dst io.Writer, data []byte) error {
	n, err := dst.Write(data)
	if err != nil {
		return err
	}
	if n != len(data) {
		return io.ErrShortWrite
	}
	return nil
}

func hdrRead(src io.Reader) (Header, [frameHeaderSize]byte, error) {
	var h Header
	var raw [frameHeaderSize]byte
	if err := frameReadFull(src, raw[:]); err != nil {
		return h, raw, err
	}
	if string(raw[:8]) != "MCODEBK1" {
		return h, raw, ErrInvalidArchive
	}
	h.Version = binary.BigEndian.Uint16(raw[8:10])
	if h.Version != 1 {
		return h, raw, ErrUnsupportedVersion
	}
	h.Time = binary.BigEndian.Uint32(raw[10:14])
	h.MemoryKiB = binary.BigEndian.Uint32(raw[14:18])
	h.Threads = raw[18]
	copy(h.Salt[:], raw[19:35])
	if h.Time < 1 || h.Time > 6 || h.MemoryKiB < 8192 || h.MemoryKiB > 262144 || h.Threads < 1 || h.Threads > 8 {
		return h, raw, ErrInvalidArchive
	}
	return h, raw, nil
}

func ReadHeader(src io.Reader) (Header, error) {
	h, _, err := hdrRead(src)
	return h, err
}

func frameKey(password []byte, h Header) []byte {
	return argon2.IDKey(password, h.Salt[:], h.Time, h.MemoryKiB, h.Threads, chacha20poly1305.KeySize)
}

func frameNonce(raw [frameHeaderSize]byte, index uint64) [chacha20poly1305.NonceSizeX]byte {
	var nonce [chacha20poly1305.NonceSizeX]byte
	copy(nonce[:16], raw[35:51])
	binary.BigEndian.PutUint64(nonce[16:], index)
	return nonce
}

func frameAD(raw [frameHeaderSize]byte, index uint64, prefix []byte) []byte {
	ad := make([]byte, frameHeaderSize+8+5)
	copy(ad, raw[:])
	binary.BigEndian.PutUint64(ad[frameHeaderSize:], index)
	copy(ad[frameHeaderSize+8:], prefix)
	return ad
}

func Encrypt(ctx context.Context, dst io.Writer, password []byte, src io.Reader) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	h := Header{Version: 1, Time: 3, MemoryKiB: 65536, Threads: 1}
	if frameTestParams != nil {
		h.Time, h.MemoryKiB, h.Threads = frameTestParams.Time, frameTestParams.MemoryKiB, frameTestParams.Threads
	}
	var raw [frameHeaderSize]byte
	copy(raw[:8], "MCODEBK1")
	binary.BigEndian.PutUint16(raw[8:10], h.Version)
	binary.BigEndian.PutUint32(raw[10:14], h.Time)
	binary.BigEndian.PutUint32(raw[14:18], h.MemoryKiB)
	raw[18] = h.Threads
	if _, err := io.ReadFull(rand.Reader, raw[19:]); err != nil {
		return err
	}
	copy(h.Salt[:], raw[19:35])
	key := frameKey(password, h)
	defer clear(key)
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return err
	}
	if err := frameWrite(dst, raw[:]); err != nil {
		return err
	}
	buf := make([]byte, frameChunkSize)
	for index := uint64(0); ; index++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, readErr := io.ReadFull(src, buf)
		if readErr != nil && !errors.Is(readErr, io.EOF) && !errors.Is(readErr, io.ErrUnexpectedEOF) {
			return readErr
		}
		var prefix [5]byte
		binary.BigEndian.PutUint32(prefix[:4], uint32(n))
		if readErr != nil {
			prefix[4] = 1
		}
		nonce := frameNonce(raw, index)
		sealed := aead.Seal(nil, nonce[:], buf[:n], frameAD(raw, index, prefix[:]))
		if err := frameWrite(dst, prefix[:]); err != nil {
			return err
		}
		if err := frameWrite(dst, sealed); err != nil {
			return err
		}
		if prefix[4] == 1 {
			return nil
		}
		if index == math.MaxUint64 {
			return ErrInvalidArchive
		}
	}
}

func Decrypt(ctx context.Context, dst io.Writer, password []byte, src io.Reader) (Header, error) {
	if err := ctx.Err(); err != nil {
		return Header{}, err
	}
	h, raw, err := hdrRead(src)
	if err != nil {
		return h, err
	}
	key := frameKey(password, h)
	defer clear(key)
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return h, err
	}
	for index := uint64(0); ; index++ {
		if err := ctx.Err(); err != nil {
			return h, err
		}
		var prefix [5]byte
		if err := frameReadFull(src, prefix[:]); err != nil {
			return h, err
		}
		length := binary.BigEndian.Uint32(prefix[:4])
		if length > frameChunkSize || prefix[4] > 1 {
			return h, ErrInvalidArchive
		}
		sealed := make([]byte, int(length)+aead.Overhead())
		if err := frameReadFull(src, sealed); err != nil {
			return h, err
		}
		nonce := frameNonce(raw, index)
		plain, err := aead.Open(nil, nonce[:], sealed, frameAD(raw, index, prefix[:]))
		if err != nil {
			if index == 0 {
				return h, ErrWrongPassword
			}
			return h, ErrInvalidArchive
		}
		if err := frameWrite(dst, plain); err != nil {
			return h, err
		}
		if prefix[4] == 1 {
			var extra [1]byte
			n, err := io.ReadFull(src, extra[:])
			if n != 0 {
				return h, ErrInvalidArchive
			}
			if err == io.EOF {
				return h, nil
			}
			return h, err
		}
		if index == math.MaxUint64 {
			return h, ErrInvalidArchive
		}
	}
}
