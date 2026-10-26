// Package dashboard serves the embedded single-page admin UI.
package dashboard

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed web
var assets embed.FS

func Handler() http.Handler {
	sub, _ := fs.Sub(assets, "web")
	return http.FileServer(http.FS(sub))
}
