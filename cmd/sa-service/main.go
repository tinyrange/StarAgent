package main

import (
	"fmt"
	"os"

	"github.com/tinyrange/staragent/internal/service"
)

func main() {
	if err := service.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "fatal error: %v\n", err)
		os.Exit(1)
	}
}
