package web

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed mobius loki common
var assets embed.FS

func Handler(app string) http.Handler {
	root, err := fs.Sub(assets, app)
	if err != nil {
		panic(err)
	}
	return http.FileServer(http.FS(root))
}
