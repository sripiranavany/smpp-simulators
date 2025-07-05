package main

import (
	"bufio"
	"encoding/json" // Import for JSON parsing
	"errors"        // <--- ADDED: for errors.Is
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"strings"
	"time" // <--- ADDED: for time.Sleep and SetReadDeadline
)

// UserConfig holds the user simulator configuration parameters
type UserConfig struct {
	ServerAddr string `json:"ServerAddr"`
	ServerPort int    `json:"ServerPort"` // This will be the UserPort of the SMPP server
}

// LoadUserConfig reads the user simulator configuration from a JSON file
func LoadUserConfig(filePath string) (*UserConfig, error) {
	file, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read user config file %s: %w", filePath, err)
	}

	var config UserConfig
	err = json.Unmarshal(file, &config)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal user config JSON from %s: %w", filePath, err)
	}

	return &config, nil
}

// receiveMessages listens for messages from the server and prints them
// It now accepts a stop channel to signal graceful exit.
func receiveMessages(conn net.Conn, stopChan <-chan struct{}) { // <--- MODIFIED: Added stopChan parameter
	reader := bufio.NewReader(conn)
	for {
		select {
		case <-stopChan: // <--- NEW: Listen for stop signal
			log.Println("Stopping message receiver goroutine.")
			return // Exit the goroutine gracefully
		default:
			// Continue reading
		}

		// <--- NEW: Set a short read deadline. This prevents ReadString from blocking indefinitely
		// and allows the select statement to check stopChan regularly.
		if err := conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond)); err != nil {
			log.Printf("Error setting read deadline for user receiver: %v", err)
			return // Critical error, exit
		}

		message, err := reader.ReadString('\n') // Reads until newline
		if err != nil {
			// <--- NEW: Check if the error is a timeout from the deadline
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				continue // It was just a timeout, loop again to check stop signal
			}
			// Handle actual connection errors (EOF, network errors)
			if errors.Is(err, io.EOF) { // <--- Use errors.Is for EOF
				log.Println("Server closed the connection gracefully.")
			} else {
				log.Printf("Error receiving message from server: %v", err)
			}
			break // Exit loop on real error
		}

		// <--- NEW: Clear the read deadline after a successful read
		// This is important because the next read should not be prematurely timed out
		// if the server is slow but still sending data.
		if err := conn.SetReadDeadline(time.Time{}); err != nil {
			log.Printf("Error clearing read deadline: %v", err)
			// Not critical enough to exit, but log it
		}

		// Parse the incoming message if it follows a specific protocol
		// Expected format from server: FROM_SMSC|DEST_ADDR|TYPE|MESSAGE
		parts := strings.SplitN(strings.TrimSpace(message), "|", 4)
		if len(parts) == 4 && parts[0] == "FROM_SMSC" {
			fmt.Printf("\n--- INCOMING MESSAGE from SMSC for %s ---\n", parts[1])
			fmt.Printf("Type: %s\n", parts[2])
			fmt.Printf("Message: %s\n", parts[3])
			fmt.Print("> ") // Reprint prompt after message
		} else {
			fmt.Printf("\n--- RAW INCOMING: %s\n", strings.TrimSpace(message))
			fmt.Print("> ") // Reprint prompt after message
		}
	}
}

func main() {
	// Load configuration
	config, err := LoadUserConfig("user_config.json") // Assumes config file is in the same directory
	if err != nil {
		log.Fatalf("Failed to load user configuration: %v", err)
	}

	serverAddress := fmt.Sprintf("%s:%d", config.ServerAddr, config.ServerPort)
	conn, err := net.Dial("tcp", serverAddress) // Connect to the server's UserPort
	if err != nil {
		log.Fatalf("Failed to connect to SMPP server's user port %s: %v", serverAddress, err)
	}
	// defer conn.Close() // <--- REMOVED: We will close it manually for precise control

	log.Printf("Connected to SMPP server's user port at %s", serverAddress)
	fmt.Println("Welcome to the User Simulator!")
	fmt.Println("Enter messages in the format: FROM_NUMBER|TO_NUMBER|TYPE|MESSAGE_TEXT")
	fmt.Println("TYPE can be 'SMS' or 'USSD'")
	fmt.Println("Example SMS: +12345|98765|SMS|Hello from user!")
	fmt.Println("Example USSD: +12345|98765|USSD|*123#")
	fmt.Println("Type 'exit' to quit.")

	// <--- NEW: Create the stop channel for the receiver goroutine ---
	stopReceiving := make(chan struct{})

	// --- MODIFIED: Start goroutine to receive messages from the server, passing the stop channel ---
	go receiveMessages(conn, stopReceiving) // <--- Pass the stop channel
	// --- END MODIFIED ---

	reader := bufio.NewReader(os.Stdin) // Reads user input from terminal
	for {
		fmt.Print("> ")
		input, _ := reader.ReadString('\n') // Read until newline
		input = strings.TrimSpace(input)    // Remove leading/trailing whitespace and newline

		if strings.ToLower(input) == "exit" {
			break // Exit loop if user types 'exit'
		}

		// Send the input string followed by a newline to the server
		_, err := conn.Write([]byte(input + "\n"))
		if err != nil {
			log.Printf("Error sending message to server: %v", err)
			break
		}
	}

	log.Println("User simulator exiting...") // <--- MODIFIED: Log before actual exit and close
	// <--- NEW: Signal the receiver goroutine to stop and give it time to clean up ---
	close(stopReceiving)               // Signal the receiver goroutine to stop
	time.Sleep(100 * time.Millisecond) // Give the receiver a moment to exit (adjust if needed)
	// --- END NEW ---

	// <--- NEW: Explicitly close the connection after the receiver has stopped ---
	if conn != nil {
		if err := conn.Close(); err != nil {
			log.Printf("Error closing network connection: %v", err)
		} else {
			log.Println("Network connection closed.")
		}
	}

	log.Println("User simulator exited.") // Final exit log
}
