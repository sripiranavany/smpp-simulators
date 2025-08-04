package main

import (
	"flag"
	"log"
	"ussd_user/src/config"
	"ussd_user/src/handlers"
)

func main() {
    // Command line flags
    interactive := flag.Bool("interactive", true, "Run in interactive console mode")
    configPath := flag.String("config", "config/ussd_config.json", "Path to configuration file")
    flag.Parse()

    // Load USSD configuration
    ussdConfig, err := config.LoadConfig(*configPath)
    if err != nil {
        log.Fatalf("Failed to load configuration: %v", err)
    }

    if *interactive {
        // Run in interactive console mode
        log.Println("Starting USSD User Simulator in interactive mode...")
        handlers.StartInteractiveUSSD(ussdConfig)
    } else {
        log.Println("Non-interactive mode not implemented yet")
    }
}