# SMPP Simulator (Go)

A simple SMPP (Short Message Peer-to-Peer) simulator written in Go, supporting SMS and USSD flows for both MO (Mobile Originated) and MT (Mobile Terminated) messages.  
Includes:

- **SMPP Server** (handles SMPP clients, user simulators, USSD session management)
- **SMPP Client** (binds, sends/receives SMS and USSD, interactive menu)
- **User Simulator** (simulates a mobile user sending SMS/USSD to the server)

---

## Features

- SMPP v3.4 protocol support (basic subset)
- SMS and USSD MO/MT flows
- USSD session management (per user)
- Delivery receipts for SMS (optional)
- Simple user simulator (send SMS/USSD as a mobile user)
- Interactive SMPP client (send SMS/USSD, request delivery receipts)
- TLV support for USSD (ncs_id)
- Debug logging and raw PDU dumps

---

## Directory Structure

```
smpp-sim/
├── server/   # SMPP server code
│   ├── server.go
│   └── server_config.json
├── client/   # SMPP client code
│   ├── client.go
│   └── client_config.json
├── user/     # User simulator code
│   ├── user.go
│   └── user_config.json
```

---

## Quick Start

### 1. Build all components

```sh
cd smpp-sim/server && go build -o smpp-server
cd ../client && go build -o smpp-client
cd ../user && go build -o user-sim
```

### 2. Configure

Edit the `server_config.json`, `client_config.json`, and `user_config.json` files as needed.  
Example `server_config.json`:

```json
{
  "Port": 3001,
  "UserPort": 2776,
  "SystemID": "1",
  "Password": "password"
}
```

---

### 3. Run the server

```sh
cd smpp-sim/server
./smpp-server
```

---

### 4. Run the SMPP client

```sh
cd ../client
./smpp-client
```

- Use the interactive menu to send SMS or USSD, request delivery receipts, etc.

---

### 5. Run the user simulator

```sh
cd ../user
./user-sim
```

- Enter messages in the format:  
  `FROM_NUMBER|TO_NUMBER|TYPE|MESSAGE_TEXT`  
  Example SMS: `+12345|98765|SMS|Hello from user!`  
  Example USSD: `+12345|98765|USSD|*123#`  
  Type `exit` to quit.

---

## USSD Session Management

- The server tracks USSD sessions per user (MSISDN/source address).
- USSD dialogs are routed based on session state.
- Sessions are cleaned up after 10 minutes of inactivity.

---

## Delivery Receipts

- If requested, the server simulates delivery receipts for SMS.
- Receipts are sent as `deliver_sm` with ESM class 0x04.

---

## Debugging

- All components print debug logs to the console.
- The server prints raw SMPP PDUs for troubleshooting.
- Wireshark can be used to inspect SMPP traffic on the configured ports.

---

## Notes

- This is a learning/demo tool, not a production SMSC.
- Only a subset of SMPP v3.4 is implemented.
- For advanced routing, multipart SMS, UCS2, etc., extend the code as needed.

---

## License

MIT or your preferred open source license.

---

## Authors

- Sripiranavan Yogarajah
