package web

import (
	"embed"
	"io/fs"
)

//go:embed static
var embeddedFS embed.FS

// staticFS strips the "static/" prefix so files are served at the root.
var staticFS, _ = fs.Sub(embeddedFS, "static")
