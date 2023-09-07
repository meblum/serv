# `serv`

A zero-config, dependency free, pure Go, local file server that serves a directory on your file system over http and automatically reloads html files in the browser on change.

## Install
Assuming you have [downloaded go](https://go.dev/dl/), simply run

`go install github.com/meblum/serv/cmd/serv@latest`

## Usage
running the `serv` command starts a local server on port 8080 and serves the current directory. The server injects a tiny script to html documents that tells the browser to reload when a change is detected.

## Config
A main design goal is to keep this tool extremely simple. A few optional flags may be set as follows:
```
-dir string
        directory to serve (default ".")
-no-reload
        serve without reloading on file update
-port int
        port to serve on (default 8080)
```
## How it works
The server adds a tiny script to served HTML files.
The script keeps a long-running HTTP connection between the document and the server, and the server keeps a mapping of files the HTML document depends on. The server notifies the script when a file has changed and the script will reloads the page.

## Use as a Go library
The module exposes a single `FileServer` function that returns the file server as an `http.Handler`. Please see the [go doc](https://pkg.go.dev/github.com/meblum/serv) for more information

## License
```
MIT License

Copyright (c) 2023 Meir Blumenfeld

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
```