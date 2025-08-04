package models

import (
	"fmt"
	"strings"
	"time"
)

// USSDRequest represents a USSD request structure
type USSDRequest struct {
    MobileNumber string    `json:"mobile_number"`
    USSDCode     string    `json:"ussd_code"`
    SessionID    string    `json:"session_id"`
    RequestType  string    `json:"request_type"` // "initial", "continue", "end"
    Timestamp    time.Time `json:"timestamp"`
}

// USSDResponse represents a USSD response structure
type USSDResponse struct {
    SessionID    string `json:"session_id"`
    Message      string `json:"message"`
    ResponseType string `json:"response_type"` // "continue", "end"
    Status       string `json:"status"`
}

// ValidateUSSDCode checks if the USSD code is valid
func (u *USSDRequest) ValidateUSSDCode() error {
    if u.USSDCode == "" {
        return fmt.Errorf("USSD code cannot be empty")
    }
    
    // USSD codes can start with * or # and may end with # or not
    // Examples: *123#, #123, *383*567#
    if !strings.HasPrefix(u.USSDCode, "*") && !strings.HasPrefix(u.USSDCode, "#") {
        return fmt.Errorf("invalid USSD code format: must start with * or #")
    }
    
    return nil
}

// ValidateMobileNumber checks if the mobile number is valid
func (u *USSDRequest) ValidateMobileNumber() error {
    if u.MobileNumber == "" {
        return fmt.Errorf("mobile number cannot be empty")
    }
    
    // Basic validation - you can enhance this based on your requirements
    if len(u.MobileNumber) < 8 {
        return fmt.Errorf("mobile number too short")
    }
    
    return nil
}

// String returns a formatted string representation of the USSD request
func (u *USSDRequest) String() string {
    return fmt.Sprintf("USSD Request - Mobile: %s, Code: %s, Session: %s, Type: %s", 
        u.MobileNumber, u.USSDCode, u.SessionID, u.RequestType)
}