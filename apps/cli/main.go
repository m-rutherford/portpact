package main

import (
	"fmt"
	"os"
)

func main() {
	args := os.Args[1:]

	if len(args) == 0 {
		fmt.Println("CLI - A command line tool")
		fmt.Println("\nUsage: cli <command> [arguments]")
		fmt.Println("\nCommands:")
		fmt.Println("  hello    Print a greeting")
		fmt.Println("  version  Print version info")
		return
	}

	switch args[0] {
	case "hello":
		name := "World"
		if len(args) > 1 {
			name = args[1]
		}
		fmt.Printf("Hello, %s!\n", name)
	case "version":
		fmt.Println("cli v0.0.1")
	default:
		fmt.Printf("Unknown command: %s\n", args[0])
		os.Exit(1)
	}
}
