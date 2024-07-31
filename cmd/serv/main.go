package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	"github.com/meblum/serv"
)

func logInvalidPath(dirPath string) {
	notDirMsg := fmt.Sprintf("warning: %q is not an existing directory", dirPath)
	fileInfo, err := os.Stat(dirPath)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		log.Println(notDirMsg)
	case err != nil:
		log.Println(err)
	case !fileInfo.IsDir():
		log.Println(notDirMsg)
	}
}

type config struct {
	port          int
	dir           string
	reload        bool
	shutdownAfter time.Duration
}

func getOptions() config {
	defaultConfig := config{
		port:          8080,
		dir:           ".",
		reload:        true,
		shutdownAfter: time.Minute * 30,
	}

	fs := flag.NewFlagSet(filepath.Base(os.Args[0]), flag.ExitOnError)
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "Usage: %s [-options] [directory (default \".\")]\n", fs.Name())
		fs.PrintDefaults()
	}

	port := fs.Int("port", defaultConfig.port, "port to serve on")
	noReload := fs.Bool("no-reload", !defaultConfig.reload, "serve without reloading on file update")
	shutdownAfter := fs.Duration("shutdown-after", defaultConfig.shutdownAfter, "shutdown after idle time (\"0\" for no shutdown)")
	fs.Parse(os.Args[1:])
	if fs.NArg() > 1 {
		log.Fatalf("invalid arg %q (options must not be defined after file argument)", fs.Arg(1))
	}
	dir := fs.Arg(0)
	if dir == "" {
		dir = defaultConfig.dir
	}
	logInvalidPath(dir)
	return config{
		port:          *port,
		reload:        !*noReload,
		dir:           dir,
		shutdownAfter: *shutdownAfter,
	}
}

func resetTimerOnRequest(h http.Handler, resetFn func(time.Duration), d time.Duration) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		isSSE := r.URL.Path == "/" && r.Header.Get("Accept") == "text/event-stream"
		if !isSSE {
			resetFn(d)
		}
		h.ServeHTTP(w, r)
	})
}

func fileServer(config config) *http.Server {
	port := fmt.Sprintf(":%v", config.port)
	dirFS := os.DirFS(config.dir)

	if !config.reload {
		return &http.Server{
			Addr:    port,
			Handler: http.FileServerFS(dirFS),
		}
	}

	sseContext, cancelSSE := context.WithCancel(context.Background())
	srv := &http.Server{
		Addr:    port,
		Handler: serv.FileServer(sseContext, dirFS),
	}
	srv.RegisterOnShutdown(cancelSSE)
	return srv
}

func main() {
	log.SetFlags(0)
	conf := getOptions()

	interruptCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	server := fileServer(conf)
	if conf.shutdownAfter != 0 {
		timer := time.AfterFunc(conf.shutdownAfter, func() {
			log.Println("idle timeout: shutting down")
			stop()
		})
		defer timer.Stop()
		server.Handler = resetTimerOnRequest(server.Handler, func(d time.Duration) {
			timer.Reset(d)
		}, conf.shutdownAfter)
	}

	go func() {
		log.Printf("serving files in \"%v\" at http://localhost%v", conf.dir, server.Addr)
		err := server.ListenAndServe()
		if err != http.ErrServerClosed {
			log.Println(err)
			os.Exit(1)
		}
	}()

	<-interruptCtx.Done()
	shutDownCtx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	defer cancel()
	if err := server.Shutdown(shutDownCtx); err != nil {
		log.Println(err)
		os.Exit(1)
	}
}
