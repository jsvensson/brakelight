package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/jsvensson/brakelight/internal/config"
	"github.com/jsvensson/brakelight/internal/db"
	"github.com/jsvensson/brakelight/internal/scanner"
	"github.com/jsvensson/brakelight/internal/server"
	"github.com/jsvensson/brakelight/internal/worker"
)

func main() {
	var configPath string
	flag.StringVar(&configPath, "config", "", "Path to HCL config file")
	flag.Parse()

	if len(configPath) == 0 {
		fmt.Fprintln(os.Stderr, "Usage: brakelight -config <path>")
		os.Exit(1)
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	outputDirs := map[string]bool{}
	for _, w := range cfg.Watch {
		outputDirs[w.OutputDir] = true
	}
	for dir := range outputDirs {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			log.Fatalf("Failed to create output dir %s: %v", dir, err)
		}
	}

	dbPath, err := cfg.Config.DBPath()
	if err != nil {
		log.Fatalf("Failed to resolve database path: %v", err)
	}

	database, err := db.Open(dbPath)
	if err != nil {
		log.Fatalf("Failed to open database: %v", err)
	}
	defer database.Close()

	handbrakePath, err := findHandBrakeCLI(cfg.Config.HandBrakeCLI)
	if err != nil {
		log.Fatalf("Failed to locate HandBrakeCLI: %v", err)
	}

	log.Printf("Using HandBrakeCLI: %s", handbrakePath)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w := worker.New(database, cfg, handbrakePath)
	s := scanner.New(database, cfg)
	srv := server.New(database, cfg, w.Progress())

	go w.Run(ctx)
	go s.Run(ctx)

	go func() {
		if err := srv.Start(); err != nil {
			log.Fatalf("HTTP server error: %v", err)
		}
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig

	log.Println("Shutting down...")
	cancel()

	if err := srv.Stop(ctx); err != nil {
		log.Printf("HTTP server shutdown error: %v", err)
	}
}

func findHandBrakeCLI(override string) (string, error) {
	if len(override) > 0 {
		if _, err := os.Stat(override); err == nil {
			return override, nil
		}
		return "", fmt.Errorf("configured HandBrakeCLI not found: %s", override)
	}

	candidates := []string{
		"HandBrakeCLI",
		"/opt/homebrew/bin/HandBrakeCLI",
		"/usr/local/bin/HandBrakeCLI",
		"/Applications/HandBrake.app/Contents/MacOS/HandBrakeCLI",
	}

	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c, nil
		}
	}

	return "", fmt.Errorf("HandBrakeCLI not found")
}
