package server

import (
	"embed"
	"io/fs"
)

//go:embed web
var webAssets embed.FS

// webFS returns the embedded single page application.
func webFS() fs.FS {
	sub, err := fs.Sub(webAssets, "web")
	if err != nil {
		panic(err)
	}
	return sub
}
