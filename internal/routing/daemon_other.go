//go:build !unix

package routing

func lockRoutingState(_ string) func() { return func() {} }
