package main

import "ccp/internal/cli"

var version = "0.5.1"

func main() {
	cli.Version = version
	cli.Run()
}
