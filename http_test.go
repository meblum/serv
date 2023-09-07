package serv_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/meblum/serv"
)

func Test_FileServerInjectScript(t *testing.T) {
	injectedScript := `<script>new EventSource("/").onmessage=() => location.reload()</script>`
	tests := []struct {
		name         string
		filename     string
		fileContent  string
		expectedBody string
	}{
		{
			"empty html file",
			"empty.html",
			"",
			injectedScript,
		},
		{
			"html file with head",
			"html-head",
			"<!DOCTYPE html><head></head>",
			fmt.Sprintf("<!DOCTYPE html><head>%v</head>", injectedScript),
		},
		{
			"html file without head",
			"html-no-head",
			"<!DOCTYPE html>",
			fmt.Sprintf("%v<!DOCTYPE html>", injectedScript),
		},
		{
			"text file without extension",
			"text",
			"i am the text file",
			"i am the text file",
		},
		{
			"text file with extension",
			"text.txt",
			"i am the text.txt file",
			"i am the text.txt file",
		},
		{
			"index.html",
			"index.html",
			"",
			injectedScript,
		},
		{
			"large html file",
			"large.html",
			strings.Repeat("hello world\n", 1000),
			injectedScript + strings.Repeat("hello world\n", 1000),
		},
	}
	dir := "testdata"
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	s := serv.FileServer(context.Background(), os.DirFS(dir))

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, err := os.Create(filepath.Join(dir, tt.filename))
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			if _, err := f.WriteString(tt.fileContent); err != nil {
				t.Fatal(err)
			}
			rec := httptest.NewRecorder()
			url := "/"
			if tt.filename != "index.html" {
				url += tt.filename
			}
			s.ServeHTTP(rec, httptest.NewRequest("GET", url, nil))

			res := rec.Result()
			if code := res.StatusCode; code != 200 {
				t.Fatalf("expected response code 200, got %v", code)
			}
			if cacheValue := res.Header.Get("Cache-Control"); cacheValue != "no-cache" {
				t.Fatalf("expected response Cache-Control to be no-cache, got %v", cacheValue)
			}
			if clValue := res.Header.Get("Content-Length"); clValue != fmt.Sprint(len(tt.expectedBody)) {
				t.Fatalf("expected response Content-Length to %v, got %v", len(tt.expectedBody), clValue)
			}
			if v, _ := io.ReadAll(res.Body); string(v) != tt.expectedBody {
				t.Fatalf("expected response body %v, got %v", tt.expectedBody, string(v))
			}
		})
	}
}

func createFile(filename string, t *testing.T) {
	t.Helper()
	file, err := os.Create(filepath.Join("testdata", filename))
	if err != nil {
		t.Fatal(err)
	}
	file.Close()
}

func updateFile(filename string, t *testing.T) {
	t.Helper()
	if err := os.Chtimes(filepath.Join("testdata", filename), time.Time{}, time.Now()); err != nil {
		t.Fatal(err)
	}
}
func deleteFile(filename string, t *testing.T) {
	t.Helper()
	if err := os.Remove(filepath.Join("testdata", filename)); err != nil {
		t.Fatal(err)
	}
}

func Test_FileServerNotifyFileUpdate(t *testing.T) {
	dir := "testdata"
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	ctx, cancel := context.WithCancel(context.Background())
	ts := httptest.NewServer(serv.FileServer(ctx, os.DirFS(dir)))
	defer func() {
		cancel()
		ts.Close()
	}()

	createMapping := func() {
		requestFile("index.html", "root.html", true, ts)
		requestFile("index.js", "index.html", false, ts)
		requestFile("index-module.js", "index.js", false, ts)
	}

	updateFn := map[string]func(string, *testing.T){
		"create": createFile,
		"update": updateFile,
		"delete": deleteFile,
	}

	for _, v := range []string{"create", "update", "delete"} {
		t.Run(v, func(t *testing.T) {
			for _, f := range []string{"index-module.js", "index.html", "index.js"} {
				createMapping()
				res := requestSSE("index.html", ts, t)
				updateFn[v](f, t)
				expect := fmt.Sprintf("data: %v\n\n", f)
				if r := <-res; r != expect {
					t.Errorf("expected response body %q, got %q", expect, r)
				}
			}
			createMapping()
			res := requestSSE("index.html", ts, t)
			updateFn[v]("root.html", t)
			select {
			case r := <-res:
				t.Errorf("expected no event for root.html, got %v", r)
			case <-time.After(500 * time.Millisecond):
			}
		})
	}

}

func Test_FileServerMultiSubscribe(t *testing.T) {
	dir := "testdata"
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	ctx, cancel := context.WithCancel(context.Background())
	ts := httptest.NewServer(serv.FileServer(ctx, os.DirFS(dir)))
	defer func() {
		cancel()
		ts.Close()
	}()

	createMapping := func() {
		requestFile("index.html", "root.html", true, ts)
		requestFile("index.js", "index.html", false, ts)
		requestFile("index-module.js", "index.js", false, ts)
	}

	createMapping()
	res1 := requestSSE("index.html", ts, t)
	res2 := requestSSE("index.html", ts, t)

	createFile("index-module.js", t)
	res1E, res2E := <-res1, <-res2

	expect := fmt.Sprintf("data: %v\n\n", "index-module.js")
	if res1E != expect || res2E != expect {
		t.Errorf("expected response body %q, got res1: %q, res2: %q", expect, res1E, res2E)
	}

}

func Test_FileServerClearOnUnsubscribe(t *testing.T) {
	dir := "testdata"
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	ctx, cancel := context.WithCancel(context.Background())
	ts := httptest.NewServer(serv.FileServer(ctx, os.DirFS(dir)))
	defer func() {
		cancel()
		ts.Close()
	}()

	createMapping := func() {
		requestFile("index.html", "root.html", true, ts)
		requestFile("index.js", "index.html", false, ts)
		requestFile("index-module.js", "index.js", false, ts)
	}

	createMapping()
	res := requestSSE("index.html", ts, t)
	createFile("index-module.js", t)
	<-res
	res2 := requestSSE("index.html", ts, t)
	deleteFile("index-module.js", t)
	select {
	case r := <-res2:
		t.Errorf("expected no event for root.html, got %v", r)
	case <-time.After(500 * time.Millisecond):
	}
}

func requestSSE(refererFile string, s *httptest.Server, t *testing.T) <-chan string {
	url := s.URL + "/"
	if refererFile != "index.html" {
		url += refererFile
	}
	req, err := http.NewRequest("GET", s.URL+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Referer", refererFile)
	req.Header.Set("Connection", "keep-alive")
	res, err := s.Client().Do(req)
	if err != nil {
		panic(err)
	}
	ch := make(chan string)
	go func() {
		defer res.Body.Close()
		b := make([]byte, 50)
		n, err := res.Body.Read(b)
		if err != nil && err != io.EOF {
			panic(err)
		}
		ch <- string(b[:n])
	}()
	return ch
}

func requestFile(filename, refererFilename string, isNavigate bool, s *httptest.Server) {
	url := s.URL + "/"
	if filename != "index.html" {
		url += filename
	}
	urlReferer := s.URL + "/"
	if refererFilename != "index.html" {
		urlReferer += refererFilename
	}
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		panic(err)
	}
	if isNavigate {
		req.Header.Set("Sec-Fetch-Mode", "navigate")
	}
	req.Header.Set("Referer", urlReferer)
	if _, err := s.Client().Do(req); err != nil {
		panic(err)
	}
}
