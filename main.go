package main

import (
	"fmt"
	"github.com/benenen/dbh/internal/dbh"
	"os"
)

func main() {
	if err := dbh.NewCommand().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}
