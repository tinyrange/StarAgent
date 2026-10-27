package main

import (
	"fmt"
	"os"

	"github.com/tinyrange/staragent/internal/server"
)

func main() {
	if err := server.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "fatal error: %v\n", err)
		os.Exit(1)
	}
}
