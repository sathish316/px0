package main

import (
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Dev mode (start-dev.sh, start-ide-dev.sh): PX0_DEV_WEB names the source
// checkout. The UI is then served from its web/ directory on every request
// instead of the embedded copy, uncached, and the page polls a stamp endpoint
// so it reloads itself when those files change.
var devWebDir = os.Getenv("PX0_DEV_WEB")

func init() {
	if devWebDir != "" {
		assets = os.DirFS(devWebDir)
	}
}

// devNoStore disables browser caching of static assets in dev mode.
func devNoStore(h http.Handler) http.Handler {
	if devWebDir == "" {
		return h
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		h.ServeHTTP(w, r)
	})
}

// devWebStamp is the newest modification time (ns) of anything under web/,
// ignoring the vendor cache.
func devWebStamp() int64 {
	var newest time.Time
	filepath.WalkDir(filepath.Join(devWebDir, "web"), func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if d.Name() == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}
		if fi, err := d.Info(); err == nil && fi.ModTime().After(newest) {
			newest = fi.ModTime()
		}
		return nil
	})
	return newest.UnixNano()
}

func (s *Server) handleDevStamp(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	io.WriteString(w, strconv.FormatInt(devWebStamp(), 10))
}

// devReloadSnippet makes the page reload when the stamp changes, or when the
// server comes back after a restart.
func (s *Server) devReloadSnippet() string {
	return fmt.Sprintf(`<script>(()=>{let last=null,down=false;const url=%q;setInterval(async()=>{try{const t=await (await fetch(url,{cache:"no-store"})).text();if(down||(last!==null&&t!==last))return location.reload();last=t}catch{down=true}},700)})()</script>`, s.routePath("/api/dev/stamp"))
}

func devInjectSnippet(html, snippet string) string {
	if i := strings.LastIndex(html, "</body>"); i != -1 {
		return html[:i] + snippet + html[i:]
	}
	return html + snippet
}
