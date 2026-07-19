// +build ignore

package main

import (
	"fmt"
	"os"
	"strconv"
)

func init() {
	// Check if we're being run as a width checker
	for _, a := range os.Args {
		if a == "--width" {
			fmt.Println(repo.TermWidth())
			os.Exit(0)
		}
	}
}
