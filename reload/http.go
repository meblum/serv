// Package reload provides an http.Handler that serves files an fs.FS and provides the capability to reload HTML files when they have been updated.
// Please see the documentation on the FileServer() function
package reload

import (
	"context"
	"fmt"
	"io/fs"
	"log"
	"mime"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strconv"
)

// injectWriter is a wrapper on an http.ResponseWriter passed to http.FileServer, which injects a
// reload script to the response if content tpe is text/html
type injectWriter struct {
	http.ResponseWriter
	afterPattern *regexp.Regexp
	script       string
	isHTML       bool
	injected     bool
	written      bool
}

// newInjectWriter returns a Writer which injects a reload script after the head tag found in w
func newInjectWriter(w http.ResponseWriter) *injectWriter {
	return &injectWriter{
		ResponseWriter: w,
		script:         `<script>new EventSource("/").onmessage=() => location.reload()</script>`,
		afterPattern:   regexp.MustCompile("<head[^>]*>"),
	}
}

// Write writes the response to the client and returns the length written.
// As to conform to io.Writer, the returned count excludes the count of injected script written.
func (w *injectWriter) Write(data []byte) (int, error) {
	w.written = true
	// TODO determine where to inject in empty doc
	if !w.isHTML || w.injected {
		return w.ResponseWriter.Write(data)
	}
	w.injected = true // avoid injecting on next call to write
	insertAt := 0
	matchIndex := w.afterPattern.FindIndex(data)
	if matchIndex != nil {
		insertAt = matchIndex[1]
	}

	n1, err := w.ResponseWriter.Write(data[:insertAt])
	if err != nil {
		return n1, err
	}

	if _, err := w.ResponseWriter.Write([]byte(w.script)); err != nil {
		return n1, fmt.Errorf("inject script: %w", err)
	}

	n2, err := w.ResponseWriter.Write(data[insertAt:])
	return n1 + n2, err
}

// WriteHeader writes statusCode tot he client. This is the only place where we have a chance to override
// the content-length to count for the injected script.
func (w *injectWriter) WriteHeader(statusCode int) {
	defer w.ResponseWriter.WriteHeader(statusCode)

	ctVal := w.Header().Get("Content-Type")
	if v, _, _ := mime.ParseMediaType(ctVal); v != "text/html" {
		return
	}
	w.isHTML = true
	// delete the default length, we will be appending extra data to the returned file
	clKey := "Content-length"
	clValue := w.Header().Get(clKey)
	contentLength, _ := strconv.Atoi(clValue)
	contentLength += len(w.script)
	w.Header().Set(clKey, strconv.Itoa(contentLength))
}

// server records and keeps track of request paths to map dependencies, and forwards the request
// to inject server. If the request path is /, it routes to sse handler to notify for reloads.
type server struct {
	m          *multiplexer
	fsys       fs.FS
	fileServer http.Handler
	ctx        context.Context
}

// FileServer is a wrapper around http.FileServerFS which adds a tiny script to served HTML files.
// The script keeps a long-running HTTP connection between the document and server
// and the script will be notified when the document or any of its dependencies have changed.
// When the script receives an update notification, it will reload the page.
//
// Cancelling ctx, will close the long-running HTTP connections.
func FileServer(ctx context.Context, root fs.FS) http.Handler {
	return &server{
		m:          newMultiplexer(root),
		fsys:       root,
		fileServer: http.FileServerFS(root),
		ctx:        ctx,
	}
}

func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if err := s.registerPath(r); err != nil {
		log.Println(err)
	}

	if isRequestSSE(r) {
		s.handleSSE(w, r)
		return
	}
	s.handleFile(w, r)
}

func (s *server) handleFile(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-cache")
	iw := newInjectWriter(w)
	s.fileServer.ServeHTTP(iw, r)

	// ServeHTTP does not call write on empty files
	if !iw.written {
		iw.Write(nil)
	}
}

// registerPath registers the file path to the file change notifier, it registers it so that the referer is
// notified of changes on path. Top-level HTML paths will not be registered, since they will be registered by the request from sse
func (s *server) registerPath(r *http.Request) error {
	if r.Header.Get("Sec-Fetch-Mode") == "navigate" || r.Header.Get("Referer") == "" {
		return nil
	}
	parsedReferer, err := url.Parse(r.Header.Get("Referer"))
	if err != nil {
		return err
	}
	refererPath := clean(parsedReferer.Path)
	if isRequestSSE(r) {
		return s.m.register(loadablePath(refererPath), loaderPath(refererPath))
	}
	return s.m.register(loadablePath(refererPath), loaderPath(clean(r.URL.Path)))
}

func (s *server) handleSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		const msg = "SSE not supported"
		log.Println(msg)
		http.Error(w, msg, http.StatusInternalServerError)
		return
	}

	p, err := url.Parse(r.Header.Get("Referer"))
	if err != nil {
		log.Println(err)
		http.Error(w, err.Error(), 500)
		return
	}
	n, err := s.m.subscribe(loadablePath(clean(p.Path)))
	if err != nil {
		log.Println(err)
		http.Error(w, err.Error(), 500)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	flusher.Flush()
	defer s.m.unsubscribe(n)

	for {
		select {
		case <-r.Context().Done():
			return
		case <-s.ctx.Done():
			return
		case v := <-n.eventCh:
			if _, err := fmt.Fprintf(w, "data: %v\n\n", v); err != nil {
				log.Println(err)
				return
			}
			flusher.Flush()
		case err := <-n.errCh:
			log.Println(err)
		}
	}
}

func isRequestSSE(r *http.Request) bool {
	return r.URL.Path == "/" && r.Header.Get("Accept") == "text/event-stream"
}

// clean extracts from name the path to the file served
func clean(name string) string {
	name = path.Clean("/" + name)
	if name == "/" {
		return "index.html"
	}

	// remove leading slash
	name = name[1:]

	if name[len(name)-1] == '/' {
		return name + "/index.html"
	}
	return name
}
