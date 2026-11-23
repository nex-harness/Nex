package server

import (
	"bufio"
	"compress/gzip"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
)

var gzipWriterPool = sync.Pool{
	New: func() any {
		gw, _ := gzip.NewWriterLevel(io.Discard, gzip.BestSpeed)
		return gw
	},
}

type gzipResponseWriter struct {
	http.ResponseWriter
	gw          *gzip.Writer
	wroteHeader bool
	compressed  bool
}

func isCompressibleContentType(ct string) bool {
	ct = strings.ToLower(strings.TrimSpace(ct))
	if idx := strings.IndexByte(ct, ';'); idx >= 0 {
		ct = strings.TrimSpace(ct[:idx])
	}
	if ct == "" {
		return false
	}
	if strings.HasPrefix(ct, "text/") && ct != "text/event-stream" {
		return true
	}
	switch ct {
	case "application/json",
		"application/javascript",
		"application/x-javascript",
		"application/xml",
		"application/xhtml+xml",
		"application/wasm",
		"image/svg+xml":
		return true
	}
	return strings.HasSuffix(ct, "+json") || strings.HasSuffix(ct, "+xml")
}

func (g *gzipResponseWriter) WriteHeader(code int) {
	if g.wroteHeader {
		return
	}
	g.wroteHeader = true
	if code == http.StatusNoContent || code == http.StatusNotModified || code < 200 {
		g.ResponseWriter.WriteHeader(code)
		return
	}
	h := g.ResponseWriter.Header()
	if h.Get("Content-Encoding") == "" && isCompressibleContentType(h.Get("Content-Type")) {
		h.Del("Content-Length")
		h.Set("Content-Encoding", "gzip")
		h.Add("Vary", "Accept-Encoding")
		gw := gzipWriterPool.Get().(*gzip.Writer)
		gw.Reset(g.ResponseWriter)
		g.gw = gw
		g.compressed = true
	}
	g.ResponseWriter.WriteHeader(code)
}

func (g *gzipResponseWriter) Write(b []byte) (int, error) {
	if !g.wroteHeader {
		if g.Header().Get("Content-Type") == "" {
			g.Header().Set("Content-Type", http.DetectContentType(b))
		}
		g.WriteHeader(http.StatusOK)
	}
	if g.compressed && g.gw != nil {
		return g.gw.Write(b)
	}
	return g.ResponseWriter.Write(b)
}

func (g *gzipResponseWriter) Flush() {
	if g.compressed && g.gw != nil {
		_ = g.gw.Flush()
	}
	if f, ok := g.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (g *gzipResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if hj, ok := g.ResponseWriter.(http.Hijacker); ok {
		return hj.Hijack()
	}
	return nil, nil, http.ErrNotSupported
}

func (g *gzipResponseWriter) Unwrap() http.ResponseWriter {
	return g.ResponseWriter
}

func (g *gzipResponseWriter) close() {
	if g.gw != nil {
		_ = g.gw.Close()
		gzipWriterPool.Put(g.gw)
		g.gw = nil
	}
}

func shouldBypassGzip(r *http.Request) bool {
	if r.Method == http.MethodHead || r.Method == http.MethodOptions {
		return true
	}
	if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		return true
	}
	p := r.URL.Path
	if strings.HasPrefix(p, "/ws/") ||
		strings.HasPrefix(p, "/llm-proxy/") ||
		strings.HasPrefix(p, "/relay/") ||
		strings.HasPrefix(p, "/api/v1/blobs/") ||
		strings.HasPrefix(p, "/api/v1/runners/binary/") ||
		p == "/api/v1/ssh/binary" {
		return true
	}
	if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
		return true
	}
	if r.Header.Get("Range") != "" {
		return true
	}
	return false
}

func gzipMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if shouldBypassGzip(r) {
			next.ServeHTTP(w, r)
			return
		}
		grw := &gzipResponseWriter{ResponseWriter: w}
		defer grw.close()
		next.ServeHTTP(grw, r)
	})
}
