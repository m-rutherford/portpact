package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/org/cli/internal/broker"
	"github.com/org/cli/internal/ssm"
)

var (
	version = "0.0.1"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "connect":
		connectCmd(os.Args[2:])
	case "version":
		fmt.Printf("portpact-cli v%s\n", version)
	case "help", "-h", "--help":
		printUsage()
	default:
		fmt.Printf("Unknown command: %s\n", os.Args[1])
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println("portpact-cli - Secure database tunnel")
	fmt.Println()
	fmt.Println("Usage: portpact-cli <command> [options]")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  connect    Establish a tunnel to the database")
	fmt.Println("  version    Print version info")
	fmt.Println("  help       Show this help")
	fmt.Println()
	fmt.Println("Connect options:")
	fmt.Println("  -broker-url    Broker API URL (or PORTPACT_BROKER_URL env)")
	fmt.Println("  -api-key       API key (or PORTPACT_API_KEY env)")
	fmt.Println("  -local-port    Local port to bind (default: 5432)")
	fmt.Println("  -target        Target resource (default: rds-postgres)")
}

func connectCmd(args []string) {
	fs := flag.NewFlagSet("connect", flag.ExitOnError)
	
	brokerURL := fs.String("broker-url", os.Getenv("PORTPACT_BROKER_URL"), "Broker API URL")
	apiKey := fs.String("api-key", os.Getenv("PORTPACT_API_KEY"), "API key for broker")
	localPort := fs.Int("local-port", 5432, "Local port to bind")
	target := fs.String("target", "rds-postgres", "Target resource")
	
	fs.Parse(args)

	if *brokerURL == "" {
		log.Fatal("❌ broker-url is required (or set PORTPACT_BROKER_URL)")
	}
	if *apiKey == "" {
		log.Fatal("❌ api-key is required (or set PORTPACT_API_KEY)")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle interrupt
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		fmt.Println("\n🛑 Shutting down...")
		cancel()
	}()

	// Call broker to get session credentials
	fmt.Printf("🔐 Requesting session from broker...\n")
	client := broker.NewClient(*brokerURL, *apiKey)
	creds, err := client.GetSession(&broker.SessionRequest{
		Target:    *target,
		LocalPort: *localPort,
	})
	if err != nil {
		log.Fatalf("❌ Failed to get session: %v", err)
	}

	fmt.Printf("✅ Session acquired: %s\n", creds.SessionID)
	fmt.Printf("   Target: %s:%d\n", creds.Target.Host, creds.Target.Port)

	// Connect to SSM WebSocket
	fmt.Printf("🔌 Connecting to SSM...\n")
	session := ssm.NewSession(creds)
	if err := session.Connect(ctx); err != nil {
		log.Fatalf("❌ Failed to connect: %v", err)
	}
	defer session.Close()

	fmt.Printf("✅ SSM session established\n")

	// Start port forwarder
	forwarder := ssm.NewPortForwarder(session, *localPort)
	if err := forwarder.Start(ctx); err != nil {
		log.Fatalf("❌ Failed to start port forwarder: %v", err)
	}
	defer forwarder.Close()

	fmt.Printf("\n🚀 Tunnel ready! Connect with:\n")
	fmt.Printf("   psql -h 127.0.0.1 -p %d -U postgres -d postgres\n\n", *localPort)
	fmt.Printf("Press Ctrl+C to disconnect.\n")

	// Wait for either context cancellation or session close
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			forwarder.Close()
			forwarder.Wait()
			return
		case <-ticker.C:
			if !session.IsConnected() {
				fmt.Println("\n❌ Session disconnected")
				forwarder.Close()
				return
			}
		}
	}
}
