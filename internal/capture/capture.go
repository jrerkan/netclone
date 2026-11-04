// Package capture turns a block device into an NCI image.
package capture

import (
	"fmt"
	"io"
	"os"
	"time"

	"netclone/internal/nci"
)

// Options configures a capture job.
type Options struct {
	Source    string
	Dest      string
	Level     int
	ChunkSize int
	// Cancel is polled between chunks; closing it aborts the capture and
	// removes the partial file.
	Cancel   <-chan struct{}
	OnUpdate func(stats nci.CaptureStats)
}

// Result is the outcome of a successful capture.
type Result struct {
	nci.CaptureStats
	Header nci.Header
	Sum    string
}

// Run captures the whole source device into opts.Dest. It blocks until the
// capture is finished, canceled or failed.
func Run(opts Options) (Result, error) {
	if opts.ChunkSize <= 0 {
		opts.ChunkSize = nci.DefaultChunkSize
	}
	size, err := nci.DiskSize(opts.Source)
	if err != nil {
		return Result{}, fmt.Errorf("disk size of %s: %w", opts.Source, err)
	}
	if size <= 0 {
		return Result{}, fmt.Errorf("disk %s reports size 0", opts.Source)
	}

	src, err := os.Open(opts.Source)
	if err != nil {
		return Result{}, err
	}
	defer src.Close()

	hdr := nci.NewHeader(size, nci.SectorSize(opts.Source), nci.DetectScheme(opts.Source), opts.Source)
	w, err := nci.Create(nci.CaptureOptions{
		Dest:   opts.Dest,
		Header: hdr,
		Level:  opts.Level,
	})
	if err != nil {
		return Result{}, err
	}

	buf := make([]byte, opts.ChunkSize)
	started := time.Now()

	var (
		off      int64
		zeroRun  int64
		zeroAt   int64
		lastEmit = time.Now()
	)

	canceled := func() bool {
		if opts.Cancel == nil {
			return false
		}
		select {
		case <-opts.Cancel:
			return true
		default:
			return false
		}
	}

	flushZero := func() error {
		if zeroRun == 0 {
			return nil
		}
		if err := w.WriteZero(zeroAt, zeroRun); err != nil {
			return err
		}
		zeroRun = 0
		return nil
	}

	emit := func() {
		s := w.Stats()
		s.SpeedBps = float64(s.BytesRead) / time.Since(started).Seconds()
		if opts.OnUpdate != nil {
			opts.OnUpdate(s)
		}
		lastEmit = time.Now()
	}

	for off < size {
		if canceled() {
			w.Abort()
			return Result{}, fmt.Errorf("capture canceled")
		}
		want := int64(len(buf))
		if size-off < want {
			want = size - off
		}
		n, rerr := io.ReadFull(src, buf[:want])
		if n > 0 {
			chunk := buf[:n]
			if nci.IsZero(chunk) {
				if zeroRun == 0 {
					zeroAt = off
				}
				zeroRun += int64(n)
			} else {
				if err := flushZero(); err != nil {
					w.Abort()
					return Result{}, err
				}
				if err := w.WriteData(off, chunk); err != nil {
					w.Abort()
					return Result{}, err
				}
			}
			off += int64(n)
		}
		if time.Since(lastEmit) > 500*time.Millisecond {
			emit()
		}
		if rerr != nil {
			if rerr == io.EOF || rerr == io.ErrUnexpectedEOF {
				break
			}
			w.Abort()
			return Result{}, fmt.Errorf("read %s at %d: %w", opts.Source, off, rerr)
		}
	}
	if err := flushZero(); err != nil {
		w.Abort()
		return Result{}, err
	}
	if err := w.Close(); err != nil {
		return Result{}, err
	}
	final := w.Stats()
	final.Done = true
	final.Elapsed = time.Since(started)
	if final.Elapsed > 0 {
		final.SpeedBps = float64(final.BytesRead) / final.Elapsed.Seconds()
	}
	if opts.OnUpdate != nil {
		opts.OnUpdate(final)
	}
	return Result{CaptureStats: final, Header: hdr, Sum: w.Sum()}, nil
}
