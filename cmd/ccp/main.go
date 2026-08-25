package main

import "ccp/internal/cli"

var version = "0.6.2"

func main() {
	cli.Version = version
	cli.Run()
}
