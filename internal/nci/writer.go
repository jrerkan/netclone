package nci

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"time"
)

// CaptureOptions configures a streaming capture.
type CaptureOptions struct {
	// Dest is the path of the NCI file to create. It must not exist.
	Dest string
	// Header describes the disk being captured.
	Header Header
	// Level is the zstd compression level (1 = fastest). Default 1.
	Level int
	// ZstdPath overrides the zstd binary used for compression.
	ZstdPath string
}

// CaptureStats is a snapshot of capture progress.
type CaptureStats struct {
	BytesRead    int64
	BytesWritten int64
	DataBytes    int64
	ZeroBytes    int64
	Records      int64
	Elapsed      time.Duration
	SpeedBps     float64
	Done         bool
}

// StreamWriter serialises records onto an uncompressed io.Writer. It is the
// format core shared by the capture path and the unit tests.
type StreamWriter struct {
	out    io.Writer
	hasher hash.Hash
	buf    []byte
	stats  CaptureStats
}

// NewStreamWriter creates a record writer with a reusable buffer.
func NewStreamWriter(out io.Writer, chunkSize int) *StreamWriter {
	if chunkSize <= 0 {
		chunkSize = DefaultChunkSize
	}
	return &StreamWriter{
		out:    out,
		hasher: sha256.New(),
		buf:    make([]byte, RecHeaderSize+chunkSize+1),
	}
}

// Data writes a data record for p located at off.
func (sw *StreamWriter) Data(off int64, p []byte) error {
	need := RecHeaderSize + len(p)
	if need > len(sw.buf) {
		sw.buf = make([]byte, need)
	}
	rec := sw.buf[:need]
	rec[0] = RecData
	binary.LittleEndian.PutUint64(rec[1:], uint64(off))
	binary.LittleEndian.PutUint32(rec[9:], uint32(len(p)))
	copy(rec[RecHeaderSize:], p)

	sw.hasher.Write(rec)
	if _, err := sw.out.Write(rec); err != nil {
		return fmt.Errorf("nci: write failed: %w", err)
	}
	sw.stats.Records++
	sw.stats.DataBytes += int64(len(p))
	sw.stats.BytesRead += int64(len(p))
	return nil
}

// Zero writes one or more sparse records covering [off, off+n).
func (sw *StreamWriter) Zero(off, n int64) error {
	for n > 0 {
		step := n
		if step > MaxZeroRun {
			step = MaxZeroRun
		}
		rec := sw.buf[:RecHeaderSize]
		rec[0] = RecZero
		binary.LittleEndian.PutUint64(rec[1:], uint64(off))
		binary.LittleEndian.PutUint32(rec[9:], uint32(step))
		sw.hasher.Write(rec)
		if _, err := sw.out.Write(rec); err != nil {
			return fmt.Errorf("nci: write failed: %w", err)
		}
		sw.stats.Records++
		sw.stats.ZeroBytes += step
		sw.stats.BytesRead += step
		off += step
		n -= step
	}
	return nil
}

// End writes the end marker and the checksum record.
func (sw *StreamWriter) End() (string, error) {
	end := make([]byte, RecHeaderSize)
	end[0] = RecEnd
	sw.hasher.Write(end)
	if _, err := sw.out.Write(end); err != nil {
		return "", err
	}
	sum := sw.hasher.Sum(nil)
	rec := make([]byte, RecHeaderSize+len(sum))
	rec[0] = RecHash
	binary.LittleEndian.PutUint32(rec[9:], uint32(len(sum)))
	copy(rec[RecHeaderSize:], sum)
	if _, err := sw.out.Write(rec); err != nil {
		return "", err
	}
	return hex.EncodeToString(sum), nil
}

// Stats returns the counters accumulated so far.
func (sw *StreamWriter) Stats() CaptureStats { return sw.stats }

// Writer streams a disk into an NCI file.
type Writer struct {
	file     *os.File
	cmd      *exec.Cmd
	in       io.WriteCloser
	sw       *StreamWriter
	started  time.Time
	closed   bool
	failure  error
	tempPath string
	final    string
	sumHex   string
}

// Create starts a new capture. On success the returned Writer must be
// finished with Close (or discarded with Abort).
func Create(opts CaptureOptions) (*Writer, error) {
	if opts.Dest == "" {
		return nil, errors.New("nci: empty destination")
	}
	if opts.Level <= 0 {
		opts.Level = 1
	}
	// Write to a temporary file so a crashed capture never looks like a
	// complete image.
	tmp := opts.Dest + ".part"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	if _, err := f.Write(opts.Header.Encode()); err != nil {
		f.Close()
		os.Remove(tmp)
		return nil, err
	}

	zstd := opts.ZstdPath
	if zstd == "" {
		zstd = "zstd"
	}
	threads := runtime.NumCPU()
	if threads > 16 {
		threads = 16
	}
	cmd := exec.Command(zstd,
		"-"+strconv.Itoa(opts.Level),
		"-T"+strconv.Itoa(threads),
		"-q", "-c",
	)
	cmd.Stdout = f
	var stderr errBuffer
	cmd.Stderr = &stderr
	in, err := cmd.StdinPipe()
	if err != nil {
		f.Close()
		os.Remove(tmp)
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		f.Close()
		os.Remove(tmp)
		return nil, fmt.Errorf("nci: cannot start zstd: %w", err)
	}

	return &Writer{
		file:     f,
		cmd:      cmd,
		in:       in,
		sw:       NewStreamWriter(in, int(opts.Header.ChunkSize)),
		started:  time.Now(),
		tempPath: tmp,
		final:    opts.Dest,
	}, nil
}

// WriteData stores len(p) bytes located at off on the source disk.
func (w *Writer) WriteData(off int64, p []byte) error {
	if w.failure != nil {
		return w.failure
	}
	if err := w.sw.Data(off, p); err != nil {
		w.failure = err
		return err
	}
	return nil
}

// WriteZero records a sparse (all zero) region.
func (w *Writer) WriteZero(off, n int64) error {
	if w.failure != nil {
		return w.failure
	}
	if err := w.sw.Zero(off, n); err != nil {
		w.failure = err
		return err
	}
	return nil
}

// Close writes the end marker and checksum, then renames the temporary file.
func (w *Writer) Close() error {
	if w.closed {
		return w.failure
	}
	w.closed = true
	if w.failure != nil {
		w.Abort()
		return w.failure
	}

	sum, err := w.sw.End()
	if err != nil {
		w.Abort()
		return err
	}
	if err := w.in.Close(); err != nil {
		w.Abort()
		return err
	}
	if err := w.cmd.Wait(); err != nil {
		w.Abort()
		return fmt.Errorf("nci: compressor failed: %w", err)
	}
	if err := w.file.Sync(); err != nil {
		w.Abort()
		return err
	}
	if err := w.file.Close(); err != nil {
		os.Remove(w.tempPath)
		return err
	}
	if err := os.Rename(w.tempPath, w.final); err != nil {
		os.Remove(w.tempPath)
		return err
	}
	w.sumHex = sum
	w.sw.stats.Done = true
	w.sw.stats.Elapsed = time.Since(w.started)
	return nil
}

// Abort stops the compressor and deletes the partial file.
func (w *Writer) Abort() {
	if w.in != nil {
		w.in.Close()
	}
	if w.cmd != nil && w.cmd.Process != nil {
		w.cmd.Process.Kill()
		w.cmd.Wait()
	}
	if w.file != nil {
		w.file.Close()
	}
	os.Remove(w.tempPath)
}

// Stats returns a progress snapshot.
func (w *Writer) Stats() CaptureStats {
	s := w.sw.Stats()
	s.Elapsed = time.Since(w.started)
	if s.Elapsed > 0 {
		s.SpeedBps = float64(s.BytesRead) / s.Elapsed.Seconds()
	}
	return s
}

// Sum returns the hex encoded SHA-256 of the captured record stream.
func (w *Writer) Sum() string { return w.sumHex }

type errBuffer struct{ b []byte }

func (e *errBuffer) Write(p []byte) (int, error) {
	if len(e.b) < 8192 {
		e.b = append(e.b, p...)
	}
	return len(p), nil
}

func (e *errBuffer) String() string { return string(e.b) }
