package webui

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed dist
var files embed.FS

func Handler() http.Handler {
	root, _ := fs.Sub(files, "dist")
	static := http.FileServer(http.FS(root))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if strings.HasPrefix(r.URL.Path, "/api/") {
			http.Error(w, "接口不存在", http.StatusNotFound)
			return
		}
		name := strings.TrimPrefix(r.URL.Path, "/")
		if _, err := fs.Stat(root, name); err != nil && !strings.Contains(name, ".") {
			r.URL.Path = "/"
		}
		static.ServeHTTP(w, r)
	})
}
