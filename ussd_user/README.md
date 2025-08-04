# USSD User Application

This project is a USSD user application that allows users to configure their mobile number and input USSD codes for operation. 

## Project Structure

```
ussd_user
├── src
│   ├── main.go                # Entry point of the application
│   ├── config
│   │   └── config.go          # Configuration structure and loading logic
│   ├── handlers
│   │   └── ussd_handler.go     # Handles incoming USSD requests
│   └── models
│       └── ussd_request.go     # Data structures for USSD requests
├── config
│   └── ussd_config.json        # Configuration settings for the USSD application
├── go.mod                      # Go module definition file
├── go.sum                      # Checksums for module dependencies
└── README.md                   # Documentation for the project
```

## Setup Instructions

1. Clone the repository to your local machine.
2. Navigate to the project directory.
3. Run `go mod tidy` to install the necessary dependencies.
4. Configure your mobile number and any other settings in `config/ussd_config.json`.
5. Start the application by running `go run src/main.go`.

## Usage

- After starting the application, you can input USSD codes as prompted.
- The application will process the USSD codes and return the appropriate responses.

## Contributing

Feel free to submit issues or pull requests for any improvements or bug fixes.