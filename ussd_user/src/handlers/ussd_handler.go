package handlers

import (
	"bufio"
	"fmt"
	"log"
	"net"
	"os"
	"strings"
	"sync"
	"time"
	"ussd_user/src/config"
	"ussd_user/src/models"
)

// USSDSession represents an active USSD session
type USSDSession struct {
    SessionID        string
    MobileNumber     string
    StartTime        time.Time
    Connection       net.Conn
    InSession        bool
    ResponseBuffer   []string
    LastResponseTime time.Time
    WaitingForResponse bool
    ResponseChannel  chan string
    mu               sync.Mutex
    closed           bool
}

var activeSession *USSDSession

// StartInteractiveUSSD starts an interactive USSD session via console
func StartInteractiveUSSD(cfg *config.Config) {
    reader := bufio.NewReader(os.Stdin)
    
    // Get mobile number
    fmt.Print("Enter your mobile number (or press Enter for default): ")
    mobileInput, _ := reader.ReadString('\n')
    mobileInput = strings.TrimSpace(mobileInput)
    
    mobileNumber := cfg.DefaultMobile
    if mobileInput != "" {
        mobileNumber = mobileInput
    }
    
    fmt.Printf("Mobile number set to: %s\n", mobileNumber)
    fmt.Println("Enter USSD codes (e.g., *123#, *383*567#) or 'exit' to quit:")
    fmt.Println("For continuing a USSD session, just enter the number (e.g., 1, 2, 3)")
    
    for {
        fmt.Print("USSD> ")
        input, _ := reader.ReadString('\n')
        input = strings.TrimSpace(input)
        
        if strings.ToLower(input) == "exit" {
            // Close active session if any
            if activeSession != nil && activeSession.Connection != nil {
                activeSession.Connection.Close()
                activeSession = nil
            }
            break
        }
        
        if input == "" {
            continue
        }
        
        // Process USSD input
        err := processUSSDInput(input, mobileNumber, cfg)
        if err != nil {
            log.Printf("Error processing USSD: %v", err)
            fmt.Printf("Error: %v\n", err)
        }
    }
}

// processUSSDInput processes USSD input and communicates with SMPP server
func processUSSDInput(input, mobileNumber string, cfg *config.Config) error {
    var conn net.Conn
    var err error
    
    // Check if we have an active session and it's not closed
    if activeSession != nil && activeSession.Connection != nil && !activeSession.closed {
        conn = activeSession.Connection
    } else {
        // Create new connection
        serverAddress := fmt.Sprintf("%s:%d", cfg.ServerAddr, cfg.ServerPort)
        conn, err = net.Dial("tcp", serverAddress)
        if err != nil {
            return fmt.Errorf("failed to connect to SMPP server: %v", err)
        }
        
        // Create new session
        activeSession = &USSDSession{
            SessionID:        generateSessionID(),
            MobileNumber:     mobileNumber,
            StartTime:        time.Now(),
            Connection:       conn,
            InSession:        true,
            ResponseBuffer:   make([]string, 0),
            LastResponseTime: time.Now(),
            WaitingForResponse: false,
            ResponseChannel:  make(chan string, 10),
            closed:           false,
        }
        
        // Start listening for responses
        go listenForResponses(activeSession)
        
        fmt.Printf("Connected to SMPP server at %s\n", serverAddress)
    }
    
    // Send USSD request to server
    // Format: FROM|TO|TYPE|MESSAGE
    message := fmt.Sprintf("%s|%s|USSD|%s\n", mobileNumber, mobileNumber, input)
    
    _, err = conn.Write([]byte(message))
    if err != nil {
        // Connection failed, clean up and retry
        if activeSession != nil {
            activeSession.closed = true
            activeSession.Connection.Close()
            activeSession = nil
        }
        return fmt.Errorf("failed to send USSD request: %v", err)
    }
    
    activeSession.mu.Lock()
    activeSession.WaitingForResponse = true
    activeSession.ResponseBuffer = make([]string, 0)
    activeSession.LastResponseTime = time.Now()
    activeSession.mu.Unlock()
    
    log.Printf("Sent USSD request: %s", strings.TrimSpace(message))
    return nil
}

// listenForResponses listens for USSD responses from the server
func listenForResponses(session *USSDSession) {
    reader := bufio.NewReader(session.Connection)
    
    // Start response buffer handler
    go handleResponseBuffer(session)
    
    for {
        // IMPORTANT: Don't set any read deadline - keep connection alive indefinitely
        response, err := reader.ReadString('\n')
        if err != nil {
            log.Printf("Connection closed or error reading response: %v", err)
            // Mark session as closed
            session.mu.Lock()
            session.closed = true
            session.mu.Unlock()
            
            // Clean up session
            if activeSession == session {
                activeSession = nil
            }
            return
        }
        
        response = strings.TrimSpace(response)
        
        session.mu.Lock()
        if !session.closed {
            session.WaitingForResponse = false
            session.LastResponseTime = time.Now()
        }
        session.mu.Unlock()
        
        // Send response to channel for processing
        if !session.closed {
            select {
            case session.ResponseChannel <- response:
            default:
                // Channel full, process immediately
                go processResponse(session, response)
            }
        }
    }
}

// processResponse processes a single response
func processResponse(session *USSDSession, response string) {
    if session.closed {
        return
    }
    
    // Parse response format: FROM_SMSC|DEST_ADDR|TYPE|MESSAGE
    parts := strings.SplitN(response, "|", 4)
    if len(parts) == 4 {
        messageType := parts[2]
        message := parts[3]
        
        if messageType == "USSD" || messageType == "SMS" {
            session.mu.Lock()
            if !session.closed {
                session.ResponseBuffer = append(session.ResponseBuffer, message)
                session.LastResponseTime = time.Now()
            }
            session.mu.Unlock()
        }
    } else {
        // Handle other response formats
        session.mu.Lock()
        if !session.closed {
            session.ResponseBuffer = append(session.ResponseBuffer, response)
            session.LastResponseTime = time.Now()
        }
        session.mu.Unlock()
    }
}

// handleResponseBuffer processes responses from the channel with buffering
func handleResponseBuffer(session *USSDSession) {
    ticker := time.NewTicker(100 * time.Millisecond)
    defer ticker.Stop()
    
    for {
        select {
        case response := <-session.ResponseChannel:
            if !session.closed {
                processResponse(session, response)
            }
        case <-ticker.C:
            // Check if we should display buffered responses
            session.mu.Lock()
            shouldDisplay := len(session.ResponseBuffer) > 0 && 
                time.Since(session.LastResponseTime) > 1*time.Second &&
                !session.closed
            session.mu.Unlock()
            
            if shouldDisplay {
                displayBufferedResponses(session)
            }
            
            // Check if session is still active
            if activeSession != session || session.closed {
                return
            }
        }
    }
}

// displayBufferedResponses displays all buffered responses as a single menu
func displayBufferedResponses(session *USSDSession) {
    session.mu.Lock()
    if len(session.ResponseBuffer) == 0 || session.closed {
        session.mu.Unlock()
        return
    }
    
    // Copy buffer and clear it
    responses := make([]string, len(session.ResponseBuffer))
    copy(responses, session.ResponseBuffer)
    session.ResponseBuffer = make([]string, 0)
    session.mu.Unlock()
    
    fmt.Printf("\n")
    fmt.Println("USSD Response:")
    fmt.Println("─────────────────────────────────────")
    
    // Process and display all responses
    var welcomeMessage string
    var menuItems []string
    
    for _, response := range responses {
        cleanedResponse := cleanUSSDResponse(response)
        
        // Check if this is a welcome message
        if strings.Contains(cleanedResponse, "ආයුබෝවන්") || 
           strings.Contains(cleanedResponse, "Welcome") || 
           strings.Contains(cleanedResponse, "hSenid") {
            welcomeMessage = cleanedResponse
        } else if strings.Contains(cleanedResponse, ".") {
            // This looks like a menu item
            menuItems = append(menuItems, cleanedResponse)
        } else {
            // Other message
            menuItems = append(menuItems, cleanedResponse)
        }
    }
    
    // Display welcome message first
    if welcomeMessage != "" {
        fmt.Printf("%s\n\n", welcomeMessage)
    }
    
    // Display menu items
    for _, item := range menuItems {
        fmt.Printf("%s\n", item)
    }
    
    fmt.Println("─────────────────────────────────────")
    
    // Check if session should end
    shouldEnd := false
    for _, response := range responses {
        if isSessionEndMessage(response) {
            shouldEnd = true
            break
        }
    }
    
    if shouldEnd {
        fmt.Printf("Session ended.\n")
        session.mu.Lock()
        session.closed = true
        session.mu.Unlock()
        session.Connection.Close()
        if activeSession == session {
            activeSession = nil
        }
    } else {
        fmt.Printf("Enter your choice (or new USSD code): ")
    }
}

// cleanUSSDResponse cleans up the USSD response for better display
func cleanUSSDResponse(response string) string {
    // Replace question marks with proper characters if possible
    cleaned := strings.ReplaceAll(response, "????????", "ආයුබෝවන්")
    cleaned = strings.ReplaceAll(cleaned, "S??????", "සමාධානය")
    
    // Remove extra newlines and clean up
    lines := strings.Split(cleaned, "\n")
    var cleanLines []string
    for _, line := range lines {
        line = strings.TrimSpace(line)
        if line != "" {
            cleanLines = append(cleanLines, line)
        }
    }
    
    return strings.Join(cleanLines, "\n")
}

// isSessionEndMessage checks if the message indicates session end
func isSessionEndMessage(message string) bool {
    lowerMsg := strings.ToLower(message)
    return strings.Contains(lowerMsg, "thank you") ||
           strings.Contains(lowerMsg, "goodbye") ||
           strings.Contains(lowerMsg, "session ended") ||
           strings.Contains(lowerMsg, "service unavailable") ||
           strings.Contains(lowerMsg, "invalid") ||
           strings.Contains(lowerMsg, "error")
}

// generateSessionID generates a unique session ID
func generateSessionID() string {
    return fmt.Sprintf("ussd_%d", time.Now().UnixNano())
}

// CreateUSSDRequest creates a new USSD request
func CreateUSSDRequest(mobileNumber, ussdCode string) *models.USSDRequest {
    return &models.USSDRequest{
        MobileNumber: mobileNumber,
        USSDCode:     ussdCode,
        SessionID:    generateSessionID(),
        RequestType:  "initial",
        Timestamp:    time.Now(),
    }
}

// ProcessUSSDResponse processes a USSD response
func ProcessUSSDResponse(response string) *models.USSDResponse {
    return &models.USSDResponse{
        SessionID:    generateSessionID(),
        Message:      response,
        ResponseType: "continue",
        Status:       "success",
    }
}