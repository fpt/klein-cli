package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/fpt/klein-cli/internal/gateway"
	pkgLogger "github.com/fpt/klein-cli/pkg/logger"
)

func main() {
	configPath := flag.String("config", "", "Path to gateway config (default: $HOME/.klein/claw/config.json)")
	logLevel := flag.String("log-level", "info", "Log level (debug, info, warn, error)")
	flag.Parse()

	// Initialize logger
	out := os.Stdout
	pkgLogger.SetGlobalLoggerWithConsoleWriter(pkgLogger.LogLevel(*logLevel), out)
	logger := pkgLogger.NewLoggerWithConsoleWriter(pkgLogger.LogLevel(*logLevel), out)

	// Load config
	cfgPath := *configPath
	if cfgPath == "" {
		cfgPath = gateway.DefaultConfigPath()
	}

	cfg, err := gateway.LoadGatewayConfig(cfgPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load config from %s: %v\n", cfgPath, err)
		fmt.Fprintf(os.Stderr, "Create a config file or specify --config path\n")
		os.Exit(1)
	}

	// Create and run gateway
	gw, err := gateway.NewGateway(cfg, logger)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create gateway: %v\n", err)
		os.Exit(1)
	}
	defer gw.Close()

	// Handle shutdown signals
	ctx, cancel := context.WithCancel(context.Background())
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		sig := <-sigCh
		logger.Info("Received signal, shutting down", "signal", sig)
		cancel()
	}()

	fmt.Println("klein-claw gateway starting...")
	fmt.Printf("  Agent: %s\n", cfg.AgentAddr)
	fmt.Printf("  Skill: %s\n", cfg.DefaultSkill)
	if cfg.Discord.Token != "" {
		fmt.Println("  Discord: enabled")
	}
	if cfg.Heartbeat.Enabled {
		fmt.Printf("  Heartbeat: %s\n", cfg.Heartbeat.Interval)
	}
	fmt.Println()

	if err := gw.Run(ctx); err != nil && err != context.Canceled {
		fmt.Fprintf(os.Stderr, "Gateway error: %v\n", err)
		os.Exit(1)
	}
}
