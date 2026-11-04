package nci

import (
	"fmt"
	"io"
	"os"
	"os/exec"
)

// Decompressor streams a zstd payload through the zstd CLI.
type Decompressor struct {
	Cmd *exec.Cmd
	Out io.ReadCloser
}

// NewDecompressor starts a zstd decoder reading from r.
func NewDecompressor(r io.Reader, zstdPath string) (*Decompressor, error) {
	if zstdPath == "" {
		zstdPath = "zstd"
	}
	cmd := exec.Command(zstdPath, "-d", "-q", "-c")
	cmd.Stdin = r
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("nci: cannot start zstd: %w", err)
	}
	return &Decompressor{Cmd: cmd, Out: out}, nil
}

// Wait waits for the decoder to finish and reports its exit status.
func (d *Decompressor) Wait() error {
	io.Copy(io.Discard, d.Out)
	return d.Cmd.Wait()
}

// Kill terminates the decoder.
func (d *Decompressor) Kill() {
	if d.Cmd != nil && d.Cmd.Process != nil {
		d.Cmd.Process.Kill()
	}
}

// OpenImage opens an NCI file and returns a verified record reader.
func OpenImage(path string, zstdPath string) (Header, *RecordReader, *Decompressor, *os.File, error) {
	f, err := os.Open(path)
	if err != nil {
		return Header{}, nil, nil, nil, err
	}
	hdr, err := ReadHeader(f)
	if err != nil {
		f.Close()
		return Header{}, nil, nil, nil, err
	}
	dec, err := NewDecompressor(f, zstdPath)
	if err != nil {
		f.Close()
		return Header{}, nil, nil, nil, err
	}
	return hdr, NewRecordReader(dec.Out), dec, f, nil
}
