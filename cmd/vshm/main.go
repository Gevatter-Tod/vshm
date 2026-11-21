// vshm - Very Simple Host Monitor
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/Gevatter-Tod/vshm/internal/cli"
)

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// Handle interrupt signal
	go func() {
		<-sigChan
		fmt.Println("Received interrupt signal, shutting down...")
		cancel()
	}()

	// Parse command line arguments
	args := os.Args[1:]
	if len(args) == 0 {
		// No arguments, show version
		fmt.Println(cli.Version())
		return
	}

	switch args[0] {
	case "version", "-v", "--version":
		fmt.Println(cli.Version())
	case "validate-config":
		configPath := ""
		if len(args) >= 2 {
			configPath = args[1]
		}
		if err := cli.ValidateConfig(ctx, configPath); err != nil {
			fmt.Fprintf(os.Stderr, "validate-config: %v\n", err)
			os.Exit(1)
		}
	case "status":
		stateDir := ""
		if len(args) >= 2 {
			stateDir = args[1]
		}
		if err := cli.Status(ctx, stateDir); err != nil {
			fmt.Fprintf(os.Stderr, "status: %v\n", err)
			os.Exit(1)
		}
	case "once":
		configPath, stateDir := "", ""
		if len(args) >= 2 {
			configPath = args[1]
		}
		if len(args) >= 3 {
			stateDir = args[2]
		}
		if err := cli.RunOnce(ctx, configPath, stateDir); err != nil {
			fmt.Fprintf(os.Stderr, "once: %v\n", err)
			os.Exit(1)
		}
	case "run":
		configPath, stateDir := "", ""
		if len(args) >= 2 {
			configPath = args[1]
		}
		if len(args) >= 3 {
			stateDir = args[2]
		}
		if err := cli.RunAgent(ctx, configPath, stateDir); err != nil {
			fmt.Fprintf(os.Stderr, "run: %v\n", err)
			os.Exit(1)
		}
	case "validate-matrix":
		cliPath, configPath, roomID := "", "", ""
		if len(args) >= 2 {
			cliPath = args[1]
		}
		if len(args) >= 3 {
			configPath = args[2]
		}
		if len(args) >= 4 {
			roomID = args[3]
		}
		if err := cli.ValidateMatrix(ctx, cliPath, configPath, roomID); err != nil {
			fmt.Fprintf(os.Stderr, "validate-matrix: %v\n", err)
			os.Exit(1)
		}
	case "test-matrix":
		cliPath, roomID, message := "", "", ""
		if len(args) >= 2 {
			cliPath = args[1]
		}
		if len(args) >= 3 {
			roomID = args[2]
		}
		if len(args) >= 4 {
			message = args[3]
		}
		if err := cli.TestMatrix(ctx, cliPath, roomID, message); err != nil {
			fmt.Fprintf(os.Stderr, "test-matrix: %v\n", err)
			os.Exit(1)
		}
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", args[0])
		fmt.Fprintf(os.Stderr, "Available commands: version, validate-config, status, once, run, validate-matrix, test-matrix\n")
		os.Exit(1)
	}
}
