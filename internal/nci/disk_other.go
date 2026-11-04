//go:build !linux

package nci

import (
	"errors"
	"os"
)

var errNotLinux = errors.New("nci: block device operations require linux")

// DiskSize is only implemented on Linux.
func DiskSize(path string) (int64, error) { return 0, errNotLinux }

// FdSize is only implemented on Linux.
func FdSize(fd uintptr) (int64, error) { return 0, errNotLinux }

// SectorSize is only implemented on Linux.
func SectorSize(path string) int { return 512 }

// DetectScheme is only implemented on Linux.
func DetectScheme(path string) Scheme { return SchemeUnknown }

// ZeroRange is only implemented on Linux.
func ZeroRange(f *os.File, off, length int64) error { return errNotLinux }

// RereadPartitions is only implemented on Linux.
func RereadPartitions(path string) error { return errNotLinux }

// FlushBuffers is only implemented on Linux.
func FlushBuffers(path string) error { return errNotLinux }

// SyncDevice works on every platform that supports fsync.
func SyncDevice(f *os.File) error { return f.Sync() }
