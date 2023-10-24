package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	"github.com/meblum/serv"
)

func exitIfError(err error) {
	if err != nil {
		log.Println(err)
		os.Exit(1)
	}
}

func main() {
	log.SetFlags(0)
	port := 8080
	dir := "."
	noReload := false

	fs := flag.NewFlagSet(filepath.Base(os.Args[0]), flag.ExitOnError)
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "Usage: %s [directory (default \".\")] [-option]\n", fs.Name())
		fs.PrintDefaults()
	}
	fs.IntVar(&port, "port", port, "port to serve on")
	fs.BoolVar(&noReload, "no-reload", noReload, "serve without reloading on file update")
	fs.Parse(os.Args[1:])
	arg := fs.Arg(0)
	if arg != "" {
		dir = arg
	}
	sseContext, cancelSSE := context.WithCancel(context.Background())
	defer cancelSSE()
	fServer := serv.FileServer(sseContext, os.DirFS(dir))
	if noReload {
		fServer = http.FileServer(http.FS(os.DirFS(dir)))
	}
	http.Handle("/", fServer)

	srv := &http.Server{Addr: fmt.Sprintf(":%v", port)}
	srv.RegisterOnShutdown(cancelSSE)
	shutdown := make(chan os.Signal, 1)
	signal.Notify(shutdown, os.Interrupt)
	go func() {
		log.Printf("serving files in \"%v\" at http://localhost%v", dir, srv.Addr)
		err := srv.ListenAndServe()
		if err != http.ErrServerClosed {
			log.Println(err)
			os.Exit(1)
		}
	}()

	<-shutdown
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	defer cancel()
	exitIfError(srv.Shutdown(ctx))
}
