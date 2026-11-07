package backup

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"testing"
)

var frameTestError = errors.New("test io error")

type frameErrorReader struct{}

func (frameErrorReader) Read([]byte) (int, error) { return 0, frameTestError }

type frameErrorWriter struct{}

func (frameErrorWriter) Write([]byte) (int, error) { return 0, frameTestError }

func frameSetup(t *testing.T) {
	t.Helper()
	old := frameTestParams
	frameTestParams = &Header{Time: 1, MemoryKiB: 8192, Threads: 1}
	t.Cleanup(func() { frameTestParams = old })
}

func frameEncryptTest(t *testing.T, data []byte) []byte {
	t.Helper()
	var out bytes.Buffer
	if err := Encrypt(context.Background(), &out, []byte("password"), bytes.NewReader(data)); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func frameDecryptTest(t *testing.T, data []byte, password string, want error) []byte {
	t.Helper()
	var out bytes.Buffer
	_, err := Decrypt(context.Background(), &out, []byte(password), bytes.NewReader(data))
	if !errors.Is(err, want) {
		t.Fatalf("Decrypt error = %v, want %v", err, want)
	}
	return out.Bytes()
}

func TestFormatRoundTrip(t *testing.T) {
	frameSetup(t)
	for _, size := range []int{0, 64 * 1024, 200 * 1024} {
		data := bytes.Repeat([]byte{0x42}, size)
		encoded := frameEncryptTest(t, data)
		reader := bytes.NewReader(encoded)
		h, err := ReadHeader(reader)
		if err != nil || h.Version != 1 || h.Time != 1 || h.MemoryKiB != 8192 || h.Threads != 1 || reader.Len() != len(encoded)-frameHeaderSize {
			t.Fatalf("ReadHeader = %+v, %v; remaining %d", h, err, reader.Len())
		}
		if got := frameDecryptTest(t, encoded, "password", nil); !bytes.Equal(got, data) {
			t.Fatalf("round trip mismatch for %d bytes", size)
		}
	}
}

func TestFormatRandomnessAndPassword(t *testing.T) {
	frameSetup(t)
	data := []byte("secret")
	a, b := frameEncryptTest(t, data), frameEncryptTest(t, data)
	if bytes.Equal(a, b) {
		t.Fatal("encrypted archives are identical")
	}
	frameDecryptTest(t, a, "wrong", ErrWrongPassword)
}

func TestFormatTampering(t *testing.T) {
	frameSetup(t)
	data := frameEncryptTest(t, bytes.Repeat([]byte{7}, 200*1024))
	frameSize := 5 + frameChunkSize + 16
	for _, tc := range []struct {
		name string
		offset int
		want error
	}{
		{"header", 35, ErrWrongPassword},
		{"first", frameHeaderSize + 5, ErrWrongPassword},
		{"middle", frameHeaderSize + frameSize + 5, ErrInvalidArchive},
		{"last", len(data) - 1, ErrInvalidArchive},
	} {
		t.Run(tc.name, func(t *testing.T) {
			changed := bytes.Clone(data)
			changed[tc.offset] ^= 1
			frameDecryptTest(t, changed, "password", tc.want)
		})
	}
}

func TestFormatMalformedFrames(t *testing.T) {
	frameSetup(t)
	data := frameEncryptTest(t, bytes.Repeat([]byte{9}, 200*1024))
	frameSize := 5 + frameChunkSize + 16
	first := frameHeaderSize
	second := first + frameSize
	third := second + frameSize
	swapped := bytes.Clone(data)
	copy(swapped[second:third], data[third:third+frameSize])
	copy(swapped[third:third+frameSize], data[second:third])
	repeated := append(bytes.Clone(data[:third]), data[second:third]...)
	repeated = append(repeated, data[third:]...)
	oversized := bytes.Clone(data)
	binary.BigEndian.PutUint32(oversized[first:first+4], frameChunkSize+1)
	badFlag := bytes.Clone(data)
	badFlag[first+4] = 2
	for _, tc := range []struct {
		name string
		data []byte
	}{
		{"truncated header", data[:10]},
		{"truncated frame", data[:len(data)-1]},
		{"missing final frame", data[:third]},
		{"appended byte", append(bytes.Clone(data), 1)},
		{"swapped frames", swapped},
		{"repeated frame", repeated},
		{"oversized frame", oversized},
		{"invalid flag", badFlag},
	} {
		t.Run(tc.name, func(t *testing.T) { frameDecryptTest(t, tc.data, "password", ErrInvalidArchive) })
	}
}

func TestFormatHeaderValidation(t *testing.T) {
	frameSetup(t)
	data := frameEncryptTest(t, nil)
	for _, tc := range []struct {
		name string
		change func([]byte)
		want error
	}{
		{"magic", func(b []byte) { b[0] ^= 1 }, ErrInvalidArchive},
		{"version", func(b []byte) { binary.BigEndian.PutUint16(b[8:10], 2) }, ErrUnsupportedVersion},
		{"time low", func(b []byte) { binary.BigEndian.PutUint32(b[10:14], 0) }, ErrInvalidArchive},
		{"time high", func(b []byte) { binary.BigEndian.PutUint32(b[10:14], 7) }, ErrInvalidArchive},
		{"memory low", func(b []byte) { binary.BigEndian.PutUint32(b[14:18], 8191) }, ErrInvalidArchive},
		{"memory high", func(b []byte) { binary.BigEndian.PutUint32(b[14:18], 262145) }, ErrInvalidArchive},
		{"threads low", func(b []byte) { b[18] = 0 }, ErrInvalidArchive},
		{"threads high", func(b []byte) { b[18] = 9 }, ErrInvalidArchive},
	} {
		t.Run(tc.name, func(t *testing.T) {
			changed := bytes.Clone(data)
			tc.change(changed)
			_, err := ReadHeader(bytes.NewReader(changed))
			if !errors.Is(err, tc.want) {
				t.Fatalf("ReadHeader error = %v, want %v", err, tc.want)
			}
			frameDecryptTest(t, changed, "password", tc.want)
		})
	}
}

func TestFormatContextAndIOErrors(t *testing.T) {
	frameSetup(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Encrypt(ctx, io.Discard, nil, bytes.NewReader(nil)); !errors.Is(err, context.Canceled) {
		t.Fatalf("Encrypt canceled: %v", err)
	}
	if _, err := Decrypt(ctx, io.Discard, nil, bytes.NewReader(nil)); !errors.Is(err, context.Canceled) {
		t.Fatalf("Decrypt canceled: %v", err)
	}
	if err := Encrypt(context.Background(), io.Discard, nil, frameErrorReader{}); !errors.Is(err, frameTestError) {
		t.Fatalf("Encrypt read error: %v", err)
	}
	if err := Encrypt(context.Background(), frameErrorWriter{}, nil, bytes.NewReader(nil)); !errors.Is(err, frameTestError) {
		t.Fatalf("Encrypt write error: %v", err)
	}
	if _, err := ReadHeader(frameErrorReader{}); !errors.Is(err, frameTestError) {
		t.Fatalf("ReadHeader read error: %v", err)
	}
	encoded := frameEncryptTest(t, []byte("data"))
	if _, err := Decrypt(context.Background(), frameErrorWriter{}, []byte("password"), bytes.NewReader(encoded)); !errors.Is(err, frameTestError) {
		t.Fatalf("Decrypt write error: %v", err)
	}
	if _, err := Decrypt(context.Background(), io.Discard, []byte("password"), io.MultiReader(bytes.NewReader(encoded[:frameHeaderSize]), frameErrorReader{})); !errors.Is(err, frameTestError) {
		t.Fatalf("Decrypt read error: %v", err)
	}
}
