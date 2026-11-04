package nci

import (
	"crypto/sha256"
	"hash"
	"os"
	"time"
)

func nowUnix() int64 { return time.Now().Unix() }

func newSHA256() hash.Hash { return sha256.New() }

func openFile(path string) (*os.File, error) { return os.Open(path) }
