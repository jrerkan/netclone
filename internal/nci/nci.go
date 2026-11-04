// Package nci implements the NetClone Image (NCI) container format.
//
// An NCI file is laid out as:
//
//	[128 byte header][zstd stream of records]
//
// Every record is 13 bytes of header followed by an optional payload:
//
//	type   uint8   0=data, 1=zero, 2=end, 3=hash
//	offset uint64  byte offset inside the source disk
//	length uint32  payload length (data records) or zero-run length (zero records)
//
// A data record carries `length` bytes that must be written at `offset`.
// A zero record tells the writer to zero `length` bytes at `offset`
// (sparse regions are not transferred, which keeps images small).
//
// Records are hashed with SHA-256 (everything except the hash record itself)
// and the digest is stored in a final hash record, so a receiver can prove
// that it reconstructed exactly the byte stream the sender produced.
package nci

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
)

const (
	// Magic identifies an NCI container.
	Magic = "NCLONE01"
	// HeaderSize is the fixed size of the container header.
	HeaderSize = 128
	// Version is the container version written by this package.
	Version = 1

	// FlagZstd marks the payload as zstd compressed.
	FlagZstd uint32 = 1 << 0

	// DefaultChunkSize is the read granularity used while capturing.
	DefaultChunkSize = 4 << 20

	// Record types.
	RecData uint8 = 0
	RecZero uint8 = 1
	RecEnd  uint8 = 2
	RecHash uint8 = 3

	// RecHeaderSize is the fixed size of a record header.
	RecHeaderSize = 13

	// MaxZeroRun caps a single zero record (kept below 4 GiB).
	MaxZeroRun int64 = 1 << 30

	// MaxSourceLen is the space reserved for the source description.
	MaxSourceLen = 80
)

// Scheme is the partition table scheme of the captured disk.
type Scheme uint32

const (
	SchemeUnknown Scheme = 0
	SchemeMBR     Scheme = 1
	SchemeGPT     Scheme = 2
)

func (s Scheme) String() string {
	switch s {
	case SchemeMBR:
		return "mbr"
	case SchemeGPT:
		return "gpt"
	default:
		return "unknown"
	}
}

// Header describes the captured disk.
type Header struct {
	Version     uint32
	Flags       uint32
	DiskSize    uint64
	SectorSize  uint32
	ChunkSize   uint32
	CreatedUnix int64
	Scheme      Scheme
	Source      string
}

// NewHeader returns a header pre-filled with sensible defaults.
func NewHeader(diskSize int64, sectorSize int, scheme Scheme, source string) Header {
	if sectorSize <= 0 {
		sectorSize = 512
	}
	return Header{
		Version:     Version,
		Flags:       FlagZstd,
		DiskSize:    uint64(diskSize),
		SectorSize:  uint32(sectorSize),
		ChunkSize:   DefaultChunkSize,
		CreatedUnix: nowUnix(),
		Scheme:      scheme,
		Source:      source,
	}
}

// Encode serialises the header into its fixed 128 byte form.
func (h Header) Encode() []byte {
	b := make([]byte, HeaderSize)
	copy(b[0:8], Magic)
	binary.LittleEndian.PutUint32(b[8:], h.Version)
	binary.LittleEndian.PutUint32(b[12:], h.Flags)
	binary.LittleEndian.PutUint64(b[16:], h.DiskSize)
	binary.LittleEndian.PutUint32(b[24:], h.SectorSize)
	binary.LittleEndian.PutUint32(b[28:], h.ChunkSize)
	binary.LittleEndian.PutUint64(b[32:], uint64(h.CreatedUnix))
	binary.LittleEndian.PutUint32(b[40:], uint32(h.Scheme))
	src := h.Source
	if len(src) > MaxSourceLen {
		src = src[:MaxSourceLen]
	}
	copy(b[48:48+MaxSourceLen], src)
	return b
}

// DecodeHeader parses the fixed header.
func DecodeHeader(b []byte) (Header, error) {
	var h Header
	if len(b) < HeaderSize {
		return h, io.ErrUnexpectedEOF
	}
	if string(b[0:8]) != Magic {
		return h, fmt.Errorf("nci: bad magic %q", string(b[0:8]))
	}
	h.Version = binary.LittleEndian.Uint32(b[8:])
	if h.Version != Version {
		return h, fmt.Errorf("nci: unsupported version %d", h.Version)
	}
	h.Flags = binary.LittleEndian.Uint32(b[12:])
	h.DiskSize = binary.LittleEndian.Uint64(b[16:])
	h.SectorSize = binary.LittleEndian.Uint32(b[24:])
	h.ChunkSize = binary.LittleEndian.Uint32(b[28:])
	h.CreatedUnix = int64(binary.LittleEndian.Uint64(b[32:]))
	h.Scheme = Scheme(binary.LittleEndian.Uint32(b[40:]))
	src := b[48 : 48+MaxSourceLen]
	if i := bytes.IndexByte(src, 0); i >= 0 {
		src = src[:i]
	}
	h.Source = string(src)
	return h, nil
}

// ReadHeader reads and validates the header from r.
func ReadHeader(r io.Reader) (Header, error) {
	buf := make([]byte, HeaderSize)
	if _, err := io.ReadFull(r, buf); err != nil {
		return Header{}, err
	}
	return DecodeHeader(buf)
}

// ReadHeaderFile reads the header of an NCI file on disk.
func ReadHeaderFile(path string) (Header, error) {
	f, err := openFile(path)
	if err != nil {
		return Header{}, err
	}
	defer f.Close()
	return ReadHeader(f)
}

// Record is a single decoded record.
type Record struct {
	Type   uint8
	Offset uint64
	// Length is the number of disk bytes covered by the record.
	Length uint64
	// Data is only set for RecData records.
	Data []byte
}

// RecordReader decodes and verifies the record stream.
type RecordReader struct {
	r        *bytes.Reader
	src      io.Reader
	hasher   hash.Hash
	verified bool
	done     bool
	sum      string
}

// NewRecordReader wraps a decompressed record stream.
func NewRecordReader(r io.Reader) *RecordReader {
	return &RecordReader{
		src:    r,
		hasher: sha256.New(),
	}
}

// Next returns the next record. io.EOF is returned after the end record.
func (rr *RecordReader) Next() (*Record, error) {
	if rr.done {
		return nil, io.EOF
	}
	var hdr [RecHeaderSize]byte
	if _, err := io.ReadFull(rr.src, hdr[:]); err != nil {
		if errors.Is(err, io.ErrUnexpectedEOF) {
			return nil, fmt.Errorf("nci: truncated stream: %w", err)
		}
		return nil, err
	}
	typ := hdr[0]
	off := binary.LittleEndian.Uint64(hdr[1:])
	n := binary.LittleEndian.Uint32(hdr[9:])

	switch typ {
	case RecData:
		data := make([]byte, n)
		if _, err := io.ReadFull(rr.src, data); err != nil {
			return nil, fmt.Errorf("nci: truncated data record at %d: %w", off, err)
		}
		rr.hasher.Write(hdr[:])
		rr.hasher.Write(data)
		return &Record{Type: RecData, Offset: off, Length: uint64(n), Data: data}, nil

	case RecZero:
		rr.hasher.Write(hdr[:])
		return &Record{Type: RecZero, Offset: off, Length: uint64(n)}, nil

	case RecEnd:
		rr.hasher.Write(hdr[:])
		sum := rr.hasher.Sum(nil)
		var hh [RecHeaderSize]byte
		if _, err := io.ReadFull(rr.src, hh[:]); err != nil {
			return nil, fmt.Errorf("nci: missing hash record: %w", err)
		}
		wantLen := binary.LittleEndian.Uint32(hh[9:])
		payload := make([]byte, wantLen)
		if _, err := io.ReadFull(rr.src, payload); err != nil {
			return nil, fmt.Errorf("nci: truncated hash record: %w", err)
		}
		if hh[0] != RecHash || wantLen != uint32(len(sum)) {
			return nil, errors.New("nci: malformed hash record")
		}
		if !bytes.Equal(payload, sum) {
			return nil, fmt.Errorf("nci: checksum mismatch (got %s, want %s)",
				hex.EncodeToString(payload), hex.EncodeToString(sum))
		}
		rr.verified = true
		rr.done = true
		rr.sum = hex.EncodeToString(sum)
		return &Record{Type: RecEnd}, nil

	default:
		return nil, fmt.Errorf("nci: unknown record type %d", typ)
	}
}

// Verified reports whether the stream ended with a valid checksum.
func (rr *RecordReader) Verified() bool { return rr.verified }

// Sum returns the hex encoded SHA-256 of the transferred payload.
func (rr *RecordReader) Sum() string { return rr.sum }

// zeroRef is a read-only block used to detect sparse regions. It is never
// written to, so sharing it across goroutines is safe.
var zeroRef = make([]byte, 1<<20)

// IsZero reports whether b consists solely of zero bytes.
func IsZero(b []byte) bool {
	for len(b) > 0 {
		n := len(b)
		if n > len(zeroRef) {
			n = len(zeroRef)
		}
		if !bytes.Equal(b[:n], zeroRef[:n]) {
			return false
		}
		b = b[n:]
	}
	return true
}
