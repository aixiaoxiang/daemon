// Example of a daemon with echo service
package main

import (
	"os"

	"github.com/aixiaoxiang/daemon"
)

const (
	// name of the service
	name = "xbookApp"
)

type executable struct {
}

func (e *executable) Start() {

}

func (e *executable) Stop() {

}

func (e *executable) Run() {

}

func main() {
	srv, err := daemon.New(name, name)
	if err != nil {
		os.Exit(1)
	}
	srv.Run(&executable{})
}
