# USSD Simulator

A USSD (Unstructured Supplementary Service Data) simulator written in Go that provides an interactive menu system for testing USSD flows. The simulator can handle USSD MO (Mobile Originated) and MT (Mobile Terminated) operations with session management and configurable menu navigation.

## Features

- **USSD Session Management**: Tracks user sessions with navigation history
- **Configurable Menu System**: Define menu structure via YAML configuration
- **Multi-language Support**: Supports Unicode text (Sinhala, Bengali, etc.)
- **Navigation Controls**: Configurable back and exit keys
- **HTTP API**: RESTful endpoints for USSD operations
- **SDP Integration**: Connects to Service Delivery Platform for message routing

---

## Directory Structure

```
ussd_simulator/
├── main.go           # Main application code
├── config.yaml       # Configuration file
└── ussd-sample       # Compiled binary
```

---

## Configuration

The application uses a `config.yaml` file for configuration. Here's the structure:

```yaml
server:
  port: 5557

sdp_server_host_url: "http://127.0.0.1:7000"

sdp_app:
  id: APP_000035
  password: 944db88ef543d83673334bff93f84483

ussd_controls:
  back_key: "999"
  exit_key: "000"
  exit_message: "Thank you for using our service!"

ussdmenu:
  items:
    "0": " ආයුබෝවන්, hSenid Mobile সমাধান\n1.Company\n2.Products\n3.Careers\n000.Exit"
    "1": "Company Details\n1.CEO\n2.Location\n3.Branches\n4.Contact\n999.Back"
    "2": "Products\n1.SDP\n2.Soltura\n999.Back"
    "3": "Careers\n1.Software Engineer\n2.Project Manager\n999.Back"
    "11": "Mr.Dinesh Saparamadu.\n999.Back"
    "12": "Nawam mawatha,Colombo, Srilanka.\n999.Back"
    "13": "hSenid mobile\n999.Back"
    "14": "info@hsenid.com\n999.Back"
    "21": "Career grade service delivery platform\n999.Back"
    "22": "Service Creation Environment\n999.Back"
    "31": "Java Software Engineer for mobile back end development ( email: careers@hsenidmobile.com ) \n999.Back"
    "32": "Experienced Project Manager ( email: careers@hsenidmobile.com ) \n999.Back"
```

### Configuration Parameters

- **server.port**: Port on which the USSD simulator listens (default: 5557)
- **sdp_server_host_url**: URL of the SDP server for message routing
- **sdp_app.id**: Application ID for SDP authentication
- **sdp_app.password**: Password for SDP authentication
- **ussd_controls.back_key**: Key code for navigating back (default: "999")
- **ussd_controls.exit_key**: Key code for exiting session (default: "000")
- **ussd_controls.exit_message**: Message displayed when exiting
- **ussdmenu.items**: Menu structure with key-value pairs

---

## Building the Application

### Prerequisites

- Go 1.19 or higher
- Linux environment (for the provided binary)

### Build Command

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o ussd-sample
```

This creates a statically linked binary that can run on any Linux x64 system without dependencies.

---

## Running the Application

### 1. Ensure config.yaml is present

Make sure your `config.yaml` file is in the same directory as the binary.

### 2. Start the application

```bash
./ussd-sample
```

The application will:

- Load the configuration from `config.yaml`
- Start the HTTP server on the configured port
- Listen for USSD MO requests on `/ussd/mo`
- Provide MT endpoint on `/ussd/send`

### 3. Verify startup

You should see output similar to:

```
[GIN-debug] POST   /ussd/mo              --> main.handleUssdMo (3 handlers)
[GIN-debug] POST   /ussd/send            --> main.handleUssdMt (3 handlers)
[GIN-debug] Listening and serving HTTP on :5557
```

---

## API Endpoints

### USSD MO (Mobile Originated) - `/ussd/mo`

Receives USSD requests from mobile users.

**Request Body:**

```json
{
  "ussdOperation": "mo-init",
  "sourceAddress": "94771234567",
  "requestId": "req123",
  "sessionId": "session456",
  "applicationId": "APP_000035",
  "message": "*383*567#",
  "encoding": "0",
  "version": "1.0"
}
```

**Response:**

```json
{
  "statusCode": "S1000",
  "statusDetail": "Success"
}
```

### USSD MT (Mobile Terminated) - `/ussd/send`

Sends USSD responses to mobile users.

**Request Body:**

```json
{
  "applicationId": "APP_000035",
  "password": "944db88ef543d83673334bff93f84483",
  "version": "1.0",
  "message": "Welcome to our service\n1.Option1\n2.Option2",
  "sessionId": "session456",
  "ussdOperation": "mt-cont",
  "destinationAddress": "94771234567",
  "encoding": "0"
}
```

---

## Menu Navigation

### Menu Structure

- **Main Menu (0)**: Root level menu
- **Sub Menus (1, 2, 3)**: First level sub-menus
- **Detail Menus (11, 12, etc.)**: Second level detail pages

### Navigation Rules

- From main menu: Input "1" navigates to menu "1"
- From sub-menu: Input "1" navigates to menu "11" (current menu + input)
- **999**: Go back to previous menu
- **000**: Exit session

### Session Management

- Each user session is tracked by session ID
- Navigation history is maintained for back functionality
- Sessions are automatically cleaned up on exit
- Invalid inputs show error message with current menu

---

## Testing

### Using cURL

Test USSD MO endpoint:

```bash
curl -X POST http://localhost:5557/ussd/mo \
  -H "Content-Type: application/json" \
  -d '{
    "ussdOperation": "mo-init",
    "sourceAddress": "94771234567",
    "requestId": "req123",
    "sessionId": "session456",
    "applicationId": "APP_000035",
    "message": "*383*567#",
    "encoding": "0",
    "version": "1.0"
  }'
```

### Integration Testing

The simulator integrates with SDP servers by forwarding USSD MT requests to the configured `sdp_server_host_url`.

---

## Deployment

### Systemd Service (Optional)

Create `/etc/systemd/system/ussd-simulator.service`:

```ini
[Unit]
Description=USSD Simulator
After=network.target

[Service]
Type=simple
User=ussd
WorkingDirectory=/opt/ussd-simulator
ExecStart=/opt/ussd-simulator/ussd-sample
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
```

Enable and start:

```bash
sudo systemctl enable ussd-simulator
sudo systemctl start ussd-simulator
```

---

## Troubleshooting

### Common Issues

1. **Config file not found**

   - Ensure `config.yaml` is in the same directory as the binary
   - Check file permissions

2. **Port already in use**

   - Change the port in `config.yaml`
   - Check for other processes using the port: `netstat -tlnp | grep 5557`

3. **SDP connection failed**
   - Verify `sdp_server_host_url` is accessible
   - Check network connectivity and firewall rules

### Debug Logging

The application provides detailed logging:

- Request processing with session details
- Menu navigation decisions
- Response sending status
- Error messages for troubleshooting

---

## Customization

### Adding New Menu Items

1. Edit `config.yaml`
2. Add new entries to `ussdmenu.items`
3. Restart the application

Example:

```yaml
ussdmenu:
  items:
    "4": "New Section\n1.New Option\n999.Back"
    "41": "New option details\n999.Back"
```

### Changing Navigation Keys

Modify `ussd_controls` section in `config.yaml`:

```yaml
ussd_controls:
  back_key: "#"
  exit_key: "*"
  exit_message: "Goodbye!"
```

---

## License

MIT License

## Author

Created for SMPP/USSD learning and testing purposes.
