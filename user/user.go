package main

import (
	"bufio"
	"encoding/json" // Import for JSON parsing
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"strings"
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
func receiveMessages(conn net.Conn) {
	reader := bufio.NewReader(conn)
	for {
		message, err := reader.ReadString('\n') // Reads until newline
		if err != nil {
			if err == io.EOF {
				log.Println("Server closed the connection gracefully.")
			} else {
				log.Printf("Error receiving message from server: %v", err)
			}
			break
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
	defer conn.Close() // Ensure connection is closed when main exits

	log.Printf("Connected to SMPP server's user port at %s", serverAddress)
	fmt.Println("Welcome to the User Simulator!")
	fmt.Println("Enter messages in the format: FROM_NUMBER|TO_NUMBER|TYPE|MESSAGE_TEXT")
	fmt.Println("TYPE can be 'SMS' or 'USSD'")
	fmt.Println("Example SMS: +12345|98765|SMS|Hello from user!")
	fmt.Println("Example USSD: +12345|98765|USSD|*123#")
	fmt.Println("Type 'exit' to quit.")

	// --- NEW: Start goroutine to receive messages from the server ---
	go receiveMessages(conn) // <--- START THIS GOROUTINE
	// --- END NEW ---

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

	log.Println("User simulator exited.")
}
