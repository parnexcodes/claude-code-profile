package main

import "ccp/internal/cli"

var version = "0.7.0"

func main() {
	cli.Version = version
	cli.Run()
}
