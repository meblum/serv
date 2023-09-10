# serv

A cross platform, zero-config, dependency free, pure Go local file server that serves a directory on your file system over HTTP and automatically reloads HTML files in the browser when the document or any of its dependencies have changed.

## Install
The simplest and easiest way to compile and install this tool is by using the [Go command](https://go.dev/dl/). Simply run

`go install github.com/meblum/serv/cmd/serv@latest`

## Usage
Running the `serv` command starts a local server that serves the current directory on port 8080. Navigating to a directory will return an auto generated index of the directory, if an index.html file is present in the directory, it will be served instead. By default, all HTML documents will be injected with a tiny script which will tell the browser to reload when a change is detected.

## Config
A main design goal was to keep this tool extremely simple. A few optional flags may be set as follows:
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
The script keeps a [long-running](https://developer.mozilla.org/en-US/docs/Web/API/Server-sent_events) HTTP connection between the document and the server, and the server keeps a mapping of files the HTML document depends on. The server notifies the script when a file has changed and the script reloads the page.

## Use as a Go module
You can use and extend the functionality of this tool. The module exposes a single `FileServer` function that returns the file server as an `http.Handler`. Please refer to the [go doc](https://pkg.go.dev/github.com/meblum/serv) for more information.

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