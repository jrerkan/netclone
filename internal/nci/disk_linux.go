//go:build linux

package nci

import (
	"fmt"
	"io"
	"os"
	"syscall"
	"unsafe"
)

const (
	blkGetSize64 = 0x80081272
	blkSSZGet    = 0x1268
	blkRRPart    = 0x125F
	blkZeroOut   = 0x1277
	blkFlushBuf  = 0x1261
)

// DiskSize returns the size of a block device in bytes.
func DiskSize(path string) (int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	return FdSize(f.Fd())
}

// FdSize returns the size of an open block device or file.
func FdSize(fd uintptr) (int64, error) {
	var size uint64
	if err := ioctlPtr(fd, blkGetSize64, unsafe.Pointer(&size)); err != nil {
		var st syscall.Stat_t
		if serr := syscall.Fstat(int(fd), &st); serr == nil && st.Size > 0 {
			return st.Size, nil
		}
		return 0, err
	}
	return int64(size), nil
}

// SectorSize returns the logical sector size of a block device.
func SectorSize(path string) int {
	f, err := os.Open(path)
	if err != nil {
		return 512
	}
	defer f.Close()
	var n uint32
	if err := ioctlPtr(f.Fd(), blkSSZGet, unsafe.Pointer(&n)); err != nil {
		return 512
	}
	if n == 0 {
		return 512
	}
	return int(n)
}

// DetectScheme inspects the first sectors of a device.
func DetectScheme(path string) Scheme {
	f, err := os.Open(path)
	if err != nil {
		return SchemeUnknown
	}
	defer f.Close()
	buf := make([]byte, 1024)
	if _, err := f.ReadAt(buf, 0); err != nil && err != io.EOF {
		return SchemeUnknown
	}
	if string(buf[512:520]) == "EFI PART" {
		return SchemeGPT
	}
	if buf[510] == 0x55 && buf[511] == 0xAA {
		return SchemeMBR
	}
	return SchemeUnknown
}

// zeroChunk is reused by ZeroRange fallbacks.
var zeroChunk = make([]byte, 1<<20)

// ZeroRange zeroes [off, off+length) using BLKZEROOUT when available.
func ZeroRange(f *os.File, off, length int64) error {
	if length <= 0 {
		return nil
	}
	args := [2]uint64{uint64(off), uint64(length)}
	if err := ioctlPtr(f.Fd(), blkZeroOut, unsafe.Pointer(&args)); err == nil {
		return nil
	}
	for length > 0 {
		n := int64(len(zeroChunk))
		if length < n {
			n = length
		}
		if _, err := f.WriteAt(zeroChunk[:n], off); err != nil {
			return err
		}
		off += n
		length -= n
	}
	return nil
}

// RereadPartitions asks the kernel to re-read the partition table.
func RereadPartitions(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return ioctlPtr(f.Fd(), blkRRPart, nil)
}

// FlushBuffers flushes the block device cache.
func FlushBuffers(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return ioctlPtr(f.Fd(), blkFlushBuf, nil)
}

// SyncDevice fsyncs an open device.
func SyncDevice(f *os.File) error { return f.Sync() }

func ioctlPtr(fd uintptr, req uintptr, arg unsafe.Pointer) error {
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, req, uintptr(arg))
	if errno != 0 {
		return fmt.Errorf("ioctl 0x%x: %w", req, errno)
	}
	return nil
}
