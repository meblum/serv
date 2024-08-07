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
	"runtime/debug"
	"slices"
	"time"

	"github.com/meblum/serv/reload"
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

func getVersion() (string, error) {
	buildInfo, ok := debug.ReadBuildInfo()
	if !ok {
		return "", errors.New("build information is available only in binaries built with module support")
	}
	if buildInfo.Main.Version != "(devel)" {
		return buildInfo.Main.Version, nil
	}
	i := slices.IndexFunc(buildInfo.Settings, func(b debug.BuildSetting) bool {
		return b.Key == "vcs.revision"
	})
	if i == -1 {
		return "", errors.New("vcs.revision not found")
	}
	return "version " + buildInfo.Settings[i].Value[0:7], nil
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
		version, err := getVersion()
		if err != nil {
			version = "version unknown"
			log.Println(err)
		}
		fmt.Fprintf(fs.Output(), "Usage (%s): %s [-options] [directory (default \".\")]\n", version, fs.Name())
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
		Handler: reload.FileServer(sseContext, dirFS),
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
		shutdownTimer := time.AfterFunc(conf.shutdownAfter, func() {
			log.Println("idle timeout: shutting down")
			stop()
		})
		defer shutdownTimer.Stop()

		logAfter := conf.shutdownAfter - 2*time.Minute
		if logAfter < 0 {
			logAfter = 0
		}
		logTimer := time.AfterFunc(logAfter, func() {
			log.Printf("warning: server will shutdown in %v unless an HTTP request is received", conf.shutdownAfter-logAfter)
		})
		defer logTimer.Stop()

		h := server.Handler
		resetShutdownMiddleware := func(w http.ResponseWriter, r *http.Request) {
			isSSE := r.URL.Path == "/" && r.Header.Get("Accept") == "text/event-stream"
			if !isSSE {
				if rescheduled := shutdownTimer.Reset(conf.shutdownAfter); !rescheduled {
					// shutdown in progress
					http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
					return
				}
				if rescheduled := logTimer.Reset(logAfter); !rescheduled {
					// warning has been given
					log.Println("shutdown reset")
				}
			}
			h.ServeHTTP(w, r)
		}
		server.Handler = http.HandlerFunc(resetShutdownMiddleware)
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
