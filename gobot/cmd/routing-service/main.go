package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

const (
	defaultHost       = "0.0.0.0"
	defaultPort       = "50051"
	defaultTSPTimeout = "5"
	defaultVRPTimeout = "30"
)

func main() {
	// Get configuration from environment
	host := getEnv("ROUTING_HOST", defaultHost)
	port := getEnv("ROUTING_PORT", defaultPort)
	tspTimeout := getEnv("TSP_TIMEOUT", defaultTSPTimeout)
	vrpTimeout := getEnv("VRP_TIMEOUT", defaultVRPTimeout)

	log.Println("Starting Routing Service Manager...")
	log.Printf("Host: %s", host)
	log.Printf("Port: %s", port)
	log.Printf("TSP Timeout: %ss", tspTimeout)
	log.Printf("VRP Timeout: %ss", vrpTimeout)

	// Find the routing service directory
	servicePath, err := findRoutingServicePath()
	if err != nil {
		log.Fatalf("Failed to find routing service: %v", err)
	}

	log.Printf("Routing service path: %s", servicePath)

	// Setup Python virtual environment if needed
	if err := setupVirtualEnv(servicePath); err != nil {
		log.Fatalf("Failed to setup virtual environment: %v", err)
	}

	// Start the Python gRPC server
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cmd, err := startRoutingService(ctx, servicePath, host, port, tspTimeout, vrpTimeout)
	if err != nil {
		log.Fatalf("Failed to start routing service: %v", err)
	}

	// Handle graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	// Wait for signal
	sig := <-sigChan
	log.Printf("Received signal %v, shutting down...", sig)

	// Cancel context to stop Python service
	cancel()

	// Wait for Python process to exit (with timeout)
	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()

	select {
	case <-done:
		log.Println("Routing service stopped gracefully")
	case <-time.After(5 * time.Second):
		log.Println("Timeout waiting for service to stop, killing process")
		if err := cmd.Process.Kill(); err != nil {
			log.Printf("Failed to kill process: %v", err)
		}
	}

	log.Println("Routing service manager stopped")
}

// findRoutingServicePath locates the routing service directory
func findRoutingServicePath() (string, error) {
	// Try to find the service relative to the binary
	executable, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("failed to get executable path: %w", err)
	}

	execDir := filepath.Dir(executable)

	// Check various possible locations
	possiblePaths := []string{
		filepath.Join(execDir, "..", "..", "services", "routing-service"),
		filepath.Join(execDir, "..", "services", "routing-service"),
		"services/routing-service",
		"./services/routing-service",
	}

	// Also check from working directory
	wd, err := os.Getwd()
	if err == nil {
		possiblePaths = append(possiblePaths, filepath.Join(wd, "services", "routing-service"))
	}

	for _, path := range possiblePaths {
		absPath, err := filepath.Abs(path)
		if err != nil {
			continue
		}

		// Check if the path exists and has the server directory
		serverPath := filepath.Join(absPath, "server", "main.py")
		if _, err := os.Stat(serverPath); err == nil {
			return absPath, nil
		}
	}

	return "", fmt.Errorf("routing service not found in any expected location")
}

// setupVirtualEnv sets up the Python virtual environment
func setupVirtualEnv(servicePath string) error {
	venvPath := filepath.Join(servicePath, "venv")

	// Check if venv already exists
	if _, err := os.Stat(venvPath); err == nil {
		log.Println("Virtual environment already exists")
		return nil
	}

	log.Println("Creating virtual environment...")

	// Create venv
	cmd := exec.Command("python3", "-m", "venv", venvPath)
	cmd.Dir = servicePath
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to create venv: %w", err)
	}

	log.Println("Installing dependencies...")

	// Install dependencies
	pipPath := filepath.Join(venvPath, "bin", "pip")
	requirementsPath := filepath.Join(servicePath, "requirements.txt")

	cmd = exec.Command(pipPath, "install", "-r", requirementsPath)
	cmd.Dir = servicePath
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to install dependencies: %w", err)
	}

	log.Println("Virtual environment setup complete")
	return nil
}

// startRoutingService starts the Python gRPC server
func startRoutingService(ctx context.Context, servicePath, host, port, tspTimeout, vrpTimeout string) (*exec.Cmd, error) {
	// Generate protobuf files if needed
	generatedPath := filepath.Join(servicePath, "generated")
	if _, err := os.Stat(generatedPath); os.IsNotExist(err) {
		log.Println("Generating protobuf files...")
		if err := generateProtos(servicePath); err != nil {
			return nil, fmt.Errorf("failed to generate protos: %w", err)
		}
	}

	// Path to Python executable in venv
	pythonPath := filepath.Join(servicePath, "venv", "bin", "python3")
	mainPath := filepath.Join(servicePath, "server", "main.py")

	// Create command
	cmd := exec.CommandContext(ctx, pythonPath, mainPath,
		"--host", host,
		"--port", port,
		"--tsp-timeout", tspTimeout,
		"--vrp-timeout", vrpTimeout,
	)

	cmd.Dir = servicePath
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	// Start the process
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to start Python service: %w", err)
	}

	log.Printf("Routing service started with PID %d", cmd.Process.Pid)

	// Give the service a moment to start
	time.Sleep(2 * time.Second)

	return cmd, nil
}

// generateProtos generates Python protobuf files
func generateProtos(servicePath string) error {
	scriptPath := filepath.Join(servicePath, "generate_protos.sh")

	cmd := exec.Command("bash", scriptPath)
	cmd.Dir = servicePath
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	return cmd.Run()
}

// getEnv gets an environment variable or returns a default value
func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}
