package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"sync"

	"github.com/gin-gonic/gin"
	"gopkg.in/yaml.v3"
)

// Structures for config and request/response
type Config struct {
    Server struct {
        Port int `yaml:"port"`
    } `yaml:"server"`
    SdpServerHostURL string `yaml:"sdp_server_host_url"`
    SdpApp struct {
        ID       string `yaml:"id"`
        Password string `yaml:"password"`
    } `yaml:"sdp_app"`
    UssdControls struct {
        BackKey     string `yaml:"back_key"`
        ExitKey     string `yaml:"exit_key"`
        ExitMessage string `yaml:"exit_message"`
    } `yaml:"ussd_controls"`
    UssdMenu struct {
        Items map[string]string `yaml:"items"`
    } `yaml:"ussdmenu"`
}

type UssdMoRequest struct {
	UssdOperation  string `json:"ussdOperation"`
	SourceAddress  string `json:"sourceAddress"`
	RequestId      string `json:"requestId"`
	SessionId      string `json:"sessionId"`
	ApplicationId  string `json:"applicationId"`
	Message        string `json:"message"`
	Encoding       string `json:"encoding"`
	Version        string `json:"version"`
}

type UssdMoResponse struct {
	StatusCode   string `json:"statusCode"`
	StatusDetail string `json:"statusDetail"`
}

type UssdMtRequest struct {
	ApplicationId       string `json:"applicationId"`
	Password            string `json:"password"`
	Version             string `json:"version"`
	Message             string `json:"message"`
	SessionId           string `json:"sessionId"`
	UssdOperation       string `json:"ussdOperation"`
	DestinationAddress  string `json:"destinationAddress"`
	Encoding            string `json:"encoding"`
}

type UssdMtResponse struct {
	RequestId    string `json:"requestId"`
	StatusDetail string `json:"statusDetail"`
	Version      string `json:"version"`
	StatusCode   string `json:"statusCode"`
}

// Session tracking
type SessionData struct {
    CurrentMenu string
    History     []string
}

var (
    config   Config
    sessions = make(map[string]*SessionData)
    sessionMutex sync.RWMutex
)

// Get or create session
func getSession(sessionId string) *SessionData {
    sessionMutex.Lock()
    defer sessionMutex.Unlock()
    
    if session, exists := sessions[sessionId]; exists {
        return session
    }
    
    // Create new session starting at main menu
    sessions[sessionId] = &SessionData{
        CurrentMenu: "0",
        History:     []string{},
    }
    return sessions[sessionId]
}


// Get menu response based on user input
func getMenuResponse(userInput string, session *SessionData) (string, string) {
    // Handle special commands
    if userInput == config.UssdControls.ExitKey {
        // Exit
        return config.UssdControls.ExitMessage, "mt-fin"
    }
    
    if userInput == config.UssdControls.BackKey {
        // Go back
        if len(session.History) > 0 {
            // Pop from history
            session.CurrentMenu = session.History[len(session.History)-1]
            session.History = session.History[:len(session.History)-1]
        } else {
            session.CurrentMenu = "0" // Default to main menu
        }
        return config.UssdMenu.Items[session.CurrentMenu], "mt-cont"
    }
    
    // Check if user input leads to a valid menu
    var nextMenu string
    if session.CurrentMenu == "0" {
        // From main menu, direct mapping
        nextMenu = userInput
    } else {
        // From sub-menus, concatenate current menu + input
        nextMenu = session.CurrentMenu + userInput
    }
    
    if menuText, exists := config.UssdMenu.Items[nextMenu]; exists {
        // Save current menu to history before moving
        session.History = append(session.History, session.CurrentMenu)
        session.CurrentMenu = nextMenu
        return menuText, "mt-cont"
    }
    
    // Invalid input, show current menu again with error message
    currentMenuText := config.UssdMenu.Items[session.CurrentMenu]
    return "Invalid option. Please try again.\n\n" + currentMenuText, "mt-cont"
}

func sendUssdResponse(sessionId, destAddr, message, operation string) error {
    mtReq := UssdMtRequest{
        ApplicationId:      config.SdpApp.ID,
        Password:           config.SdpApp.Password,
        Version:            "1.0",
        Message:            message,
        SessionId:          sessionId,
        UssdOperation:      operation,
        DestinationAddress: destAddr,
        Encoding:           "0",
    }

    body, err := json.Marshal(mtReq)
    if err != nil {
        return err
    }

    url := config.SdpServerHostURL + "/ussd/send"
    resp, err := http.Post(url, "application/json", bytes.NewReader(body))
    if err != nil {
        return err
    }
    defer resp.Body.Close()

    respData, _ := ioutil.ReadAll(resp.Body)
    fmt.Printf("Sent USSD %s, got response: %s\n", operation, string(respData))

    return nil
}

func sendUssdMenu(sessionId, destAddr string) error {
    menuMsg := config.UssdMenu.Items["0"] // Always main menu; can be dynamic
    mtReq := UssdMtRequest{
        ApplicationId:      config.SdpApp.ID,
        Password:           config.SdpApp.Password,
        Version:            "1.0",
        Message:            menuMsg,
        SessionId:          sessionId,
        UssdOperation:      "mt-cont",
        DestinationAddress: destAddr,
        Encoding:           "0",
    }

    body, err := json.Marshal(mtReq)
    if err != nil {
        return err
    }

    url := config.SdpServerHostURL + "/ussd/send"
    resp, err := http.Post(url, "application/json", bytes.NewReader(body))
    if err != nil {
        return err
    }
    defer resp.Body.Close()

    // Optional: Read and log the response from :7000
    respData, _ := ioutil.ReadAll(resp.Body)
    fmt.Println("Sent menu, got response:", string(respData))

    return nil
}

func main() {
	// Load config
	data, err := ioutil.ReadFile("config.yaml")
	if err != nil {
		fmt.Println("Config file error:", err)
		os.Exit(1)
	}
	if err := yaml.Unmarshal(data, &config); err != nil {
		fmt.Println("YAML unmarshal error:", err)
		os.Exit(1)
	}

	router := gin.Default()

	// USSD MO endpoint
	router.POST("/ussd/mo", handleUssdMo)
	// USSD MT endpoint (for notifications, optional)
	router.POST("/ussd/send", handleUssdMt)

	port := fmt.Sprintf(":%d", config.Server.Port)
	router.Run(port)
}

// USSD MO Handler
func handleUssdMo(c *gin.Context) {
    var req UssdMoRequest
    if err := c.BindJSON(&req); err != nil {
        c.JSON(http.StatusBadRequest, UssdMoResponse{"E1001", "Invalid JSON"})
        return
    }

    // Send 200 OK to the MO sender
    c.JSON(http.StatusOK, UssdMoResponse{
        StatusCode:   "S1000",
        StatusDetail: "Success",
    })

    // Process USSD request
    go func() {
        session := getSession(req.SessionId)
        
        var message, operation string
        
        fmt.Printf("Processing USSD: Operation=%s, SessionId=%s, Message=%s, CurrentMenu=%s\n", 
                   req.UssdOperation, req.SessionId, req.Message, session.CurrentMenu)
        
        if req.UssdOperation == "mo-init" {
            // Initial request - send main menu
            message = config.UssdMenu.Items["0"]
            operation = "mt-cont"
        } else if req.UssdOperation == "mo-cont" {
            // Continue request - process user input
            message, operation = getMenuResponse(req.Message, session)
            
            // Clean up session if ending
            if operation == "mt-fin" {
                sessionMutex.Lock()
                delete(sessions, req.SessionId)
                sessionMutex.Unlock()
            }
        } else {
            // Handle unexpected operation types - don't terminate session
            message = "Invalid request. Please try again.\n\n" + config.UssdMenu.Items[session.CurrentMenu]
            operation = "mt-cont"
        }
        
        fmt.Printf("Sending response: Message=%s, Operation=%s\n", message, operation)
        
        err := sendUssdResponse(req.SessionId, req.SourceAddress, message, operation)
        if err != nil {
            fmt.Printf("Failed to send USSD response: %v\n", err)
        }
    }()
}

// USSD MT Handler (Simulate USSD menu send)
func handleUssdMt(c *gin.Context) {
	var req UssdMtRequest
	if err := c.BindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, UssdMtResponse{})
		return
	}

	c.JSON(http.StatusOK, UssdMtResponse{
		RequestId:    "dummy-request-id",
		StatusDetail: "Success",
		Version:      "1.0",
		StatusCode:   "S1000",
	})
}
