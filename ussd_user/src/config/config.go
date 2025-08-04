package config

import (
	"encoding/json"
	"fmt"
	"os"
)

// Config holds the USSD user simulator configuration
type Config struct {
    ServerAddr     string `json:"server_addr"`
    ServerPort     int    `json:"server_port"`
    DefaultMobile  string `json:"default_mobile"`
    SessionTimeout int    `json:"session_timeout"` // in seconds
}

// LoadConfig reads the configuration from a JSON file
func LoadConfig(filePath string) (*Config, error) {
    file, err := os.ReadFile(filePath)
    if err != nil {
        return nil, fmt.Errorf("failed to read config file %s: %w", filePath, err)
    }

    var config Config
    err = json.Unmarshal(file, &config)
    if err != nil {
        return nil, fmt.Errorf("failed to unmarshal config JSON from %s: %w", filePath, err)
    }

    // Set default values if not provided
    if config.SessionTimeout == 0 {
        config.SessionTimeout = 300 // 5 minutes default
    }

    return &config, nil
}