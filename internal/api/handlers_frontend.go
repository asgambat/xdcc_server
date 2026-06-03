package api

import (
	"io/fs"
	"net/http"
	"os"
	"path"
	"strings"

	"xdcc-go/web"
)

// frontendFS is the embedded frontend filesystem, rooted at "dist".
var frontendFS fs.FS

func init() {
	if f, err := fs.Sub(web.Dist, "dist"); err == nil {
		frontendFS = f
	}
}

// handleFrontend serves the SPA frontend with client-side routing fallback.
func (a *API) handleFrontend(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	cleanPath := path.Clean(r.URL.Path)
	if cleanPath == "/" {
		cleanPath = "/index.html"
	}
	cleanPath = strings.TrimPrefix(cleanPath, "/")

	// Try embedded FS first (production build)
	if frontendFS != nil {
		if data, err := fs.ReadFile(frontendFS, cleanPath); err == nil {
			ct := mimeTypeByExtension(path.Ext(cleanPath))
			w.Header().Set("Content-Type", ct)
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(data)
			return
		}
		// SPA fallback
		if indexData, err := fs.ReadFile(frontendFS, "index.html"); err == nil {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(indexData)
			return
		}
	}

	// Dev-mode fallback: serve from disk
	if info, diskErr := os.Stat("web/dist/index.html"); diskErr == nil && !info.IsDir() {
		http.ServeFile(w, r, "web/dist/index.html")
		return
	}

	// Placeholder if no frontend at all
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(placeholderHTML))
}

func mimeTypeByExtension(ext string) string {
	switch ext {
	case ".html":
		return "text/html; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".js":
		return "application/javascript"
	case ".json":
		return "application/json"
	case ".png":
		return "image/png"
	case ".svg":
		return "image/svg+xml"
	case ".ico":
		return "image/x-icon"
	case ".woff2":
		return "font/woff2"
	case ".webp":
		return "image/webp"
	default:
		return "application/octet-stream"
	}
}

// placeholderHTML is shown when the frontend hasn't been built yet.
var placeholderHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>XDCC Server</title>
<style>
  body { font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif; display: flex; align-items: center; justify-content: center; min-height: 100vh; margin: 0; background: #0f0f1a; color: #e0e0e0; }
  .card { text-align: center; padding: 3rem; }
  h1 { font-size: 2.5rem; margin-bottom: 0.5rem; }
  p { color: #888; }
  .badge { display: inline-block; padding: 0.25rem 0.75rem; border-radius: 999px; background: #1a1a3e; color: #7c7cff; font-size: 0.85rem; margin-top: 1rem; }
</style>
</head>
<body>
<div class="card">
  <h1>⚡ XDCC Server</h1>
  <p>REST API is running. Build the frontend with <code>cd web && npm run build</code>.</p>
  <div class="badge">API v0.2.0</div>
</div>
</body>
</html>`
