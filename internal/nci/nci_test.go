package nci

import (
	"bytes"
	"io"
	"testing"
)

func TestHeaderRoundTrip(t *testing.T) {
	h := NewHeader(123456789, 4096, SchemeGPT, "/dev/nvme0n1")
	h.CreatedUnix = 1700000000
	got, err := DecodeHeader(h.Encode())
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.DiskSize != h.DiskSize || got.SectorSize != h.SectorSize || got.Scheme != h.Scheme || got.Source != h.Source {
		t.Fatalf("round trip mismatch: %+v != %+v", got, h)
	}
	if _, err := DecodeHeader([]byte("XXXXXXXX")); err == nil {
		t.Fatal("expected bad magic error")
	}
}

func TestStreamRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	sw := NewStreamWriter(&buf, 64)
	if err := sw.Data(0, []byte{1, 2, 3, 4}); err != nil {
		t.Fatal(err)
	}
	if err := sw.Zero(64, 640); err != nil {
		t.Fatal(err)
	}
	if err := sw.Data(704, []byte("hello netclone")); err != nil {
		t.Fatal(err)
	}
	sum, err := sw.End()
	if err != nil {
		t.Fatal(err)
	}
	if len(sum) != 64 {
		t.Fatalf("unexpected checksum length %d", len(sum))
	}

	rr := NewRecordReader(&buf)
	first, err := rr.Next()
	if err != nil || first.Type != RecData || first.Offset != 0 || first.Length != 4 {
		t.Fatalf("first record: %+v err=%v", first, err)
	}
	second, err := rr.Next()
	if err != nil || second.Type != RecZero || second.Length != 640 {
		t.Fatalf("second record: %+v err=%v", second, err)
	}
	third, err := rr.Next()
	if err != nil || third.Type != RecData || third.Offset != 704 {
		t.Fatalf("third record: %+v err=%v", third, err)
	}
	end, err := rr.Next()
	if err != nil || end.Type != RecEnd {
		t.Fatalf("end record: %+v err=%v", end, err)
	}
	if !rr.Verified() || rr.Sum() != sum {
		t.Fatalf("stream not verified: %v %s != %s", rr.Verified(), rr.Sum(), sum)
	}
	if _, err := rr.Next(); err != io.EOF {
		t.Fatalf("expected EOF, got %v", err)
	}
}

func TestTamperedStreamFails(t *testing.T) {
	var buf bytes.Buffer
	sw := NewStreamWriter(&buf, 64)
	if err := sw.Data(0, []byte("payload")); err != nil {
		t.Fatal(err)
	}
	if _, err := sw.End(); err != nil {
		t.Fatal(err)
	}
	raw := buf.Bytes()
	raw[RecHeaderSize] ^= 0xff // corrupt the payload

	rr := NewRecordReader(bytes.NewReader(raw))
	for {
		_, err := rr.Next()
		if err == nil {
			continue
		}
		if rr.Verified() {
			t.Fatal("tampered stream reported as verified")
		}
		return
	}
}

func TestZeroRunIsCapped(t *testing.T) {
	var buf bytes.Buffer
	sw := NewStreamWriter(&buf, 16)
	if err := sw.Zero(0, MaxZeroRun+10); err != nil {
		t.Fatal(err)
	}
	if _, err := sw.End(); err != nil {
		t.Fatal(err)
	}
	rr := NewRecordReader(&buf)
	r1, err := rr.Next()
	if err != nil {
		t.Fatal(err)
	}
	if r1.Type != RecZero || r1.Length != uint64(MaxZeroRun) {
		t.Fatalf("expected capped zero run, got %+v", r1)
	}
	r2, err := rr.Next()
	if err != nil {
		t.Fatal(err)
	}
	if r2.Type != RecZero || r2.Length != 10 {
		t.Fatalf("expected remainder zero run, got %+v", r2)
	}
	if r2.Offset != uint64(MaxZeroRun) {
		t.Fatalf("unexpected offset %d", r2.Offset)
	}
}

func TestIsZero(t *testing.T) {
	if !IsZero(make([]byte, 3<<20)) {
		t.Fatal("zero buffer not detected")
	}
	b := make([]byte, 3<<20)
	b[2<<20+5] = 1
	if IsZero(b) {
		t.Fatal("non zero buffer detected as zero")
	}
}
