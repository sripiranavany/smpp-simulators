package main

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"strings"
	"sync"
	"time"
)

// SMPP Command IDs
const (
	BIND_RECEIVER         = 0x00000001
	BIND_TRANSMITTER      = 0x00000002
	BIND_TRANSCEIVER      = 0x00000009
	BIND_RECEIVER_RESP    = 0x80000001
	BIND_TRANSMITTER_RESP = 0x80000002
	BIND_TRANSCEIVER_RESP = 0x80000009
	UNBIND                = 0x00000006
	UNBIND_RESP           = 0x80000006
	SUBMIT_SM             = 0x00000004
	SUBMIT_SM_RESP        = 0x80000004
	DELIVER_SM            = 0x00000005
	DELIVER_SM_RESP       = 0x80000005
	ENQUIRE_LINK          = 0x00000015
	ENQUIRE_LINK_RESP     = 0x80000015
	GENERIC_NACK          = 0x80000000
)

// SMPP Status codes
const (
	ESME_ROK        = 0x00000000
	ESME_RINVMSGLEN = 0x00000001
	ESME_RINVCMDLEN = 0x00000002
	ESME_RINVCMDID  = 0x00000003
	ESME_RINVBNDSTS = 0x00000004
	ESME_RALYBND    = 0x00000005
	ESME_RINVPASWD  = 0x0000000E
	ESME_RINVSYSID  = 0x0000000F
)

// SMPP Data Coding schemes
const (
	DC_DEFAULT = 0x00
	DC_UCS2    = 0x08
)

// SMPP ESM Class values
const (
	ESM_DEFAULT          = 0x00
	ESM_USSD             = 0x40 // USSD indication
	ESM_DELIVERY_RECEIPT = 0x04 // Delivery receipt
)

// Registered delivery flags
const (
	RD_NONE            = 0x00
	RD_SUCCESS_FAILURE = 0x01
	RD_FAILURE         = 0x02
	RD_SUCCESS         = 0x04
)

// Message states for delivery receipts
const (
	MSG_STATE_ACCEPTED      = "ACCEPTD"
	MSG_STATE_UNKNOWN       = "UNKNOWN"
	MSG_STATE_REJECTED      = "REJECTD"
	MSG_STATE_DELIVERED     = "DELIVRD"
	MSG_STATE_EXPIRED       = "EXPIRED"
	MSG_STATE_DELETED       = "DELETED"
	MSG_STATE_UNDELIVERABLE = "UNDELIV"
)

// SMPP PDU Header
type PDUHeader struct {
	CommandLength uint32
	CommandID     uint32
	CommandStatus uint32
	SequenceNo    uint32
}

// SMPP PDU
type PDU struct {
	Header PDUHeader
	Body   []byte
}

// SMPP Session
type Session struct {
	conn         net.Conn
	systemID     string
	password     string
	bound        bool
	bindType     uint32
	sequenceNo   uint32
	mutex        sync.Mutex
	lastActivity time.Time
}

// SMPP Server - add session ID counter
type SMPPServer struct {
    listener        net.Listener
    userListener    net.Listener
    sessions        map[string]*Session
    userConnections map[string]net.Conn
    mutex           sync.RWMutex
    port            int
    userPort        int
    systemID        string
    password        string
    trackedMessages map[string]*TrackedMessage
    messageMutex    sync.RWMutex
    ussdSessions    map[string]*UssdSession  // Maps MSISDN to USSD session
    sessionCounter  uint64                   // Add session counter
    sessionMutex    sync.RWMutex            // Add mutex for session operations
}

// Update the UssdSession struct to include session ID and expiry
type UssdSession struct {
    UserAddr     string
    SystemID     string
    LastActive   time.Time
    SessionID    string    // Add session ID
    ExpiryTime   time.Time // Add expiry time
    State        string    // You can use this for menu/dialog state
    CreatedAt    time.Time // Track creation time
}

// --- Configuration Struct ---
// ServerConfig holds the server configuration parameters
type ServerConfig struct {
	Port     int    `json:"Port"`
	UserPort int    `json:"UserPort"`
	SystemID string `json:"SystemID"`
	Password string `json:"Password"`
}

// Message structure for SMS/USSD
type Message struct {
	SourceAddr         string
	DestAddr           string
	ShortMessage       string
	DataCoding         uint8
	ESMClass           uint8
	IsUSSD             bool
	RegisteredDelivery uint8
}

// Delivery receipt structure
type DeliveryReceipt struct {
	MessageID    string
	SourceAddr   string
	DestAddr     string
	MessageState string
	SubmitDate   time.Time
	DoneDate     time.Time
	ErrorCode    string
	Text         string
}

// Tracked message for delivery receipts
type TrackedMessage struct {
	MessageID          string
	SourceAddr         string
	DestAddr           string
	SystemID           string
	SubmitTime         time.Time
	RegisteredDelivery uint8
	Status             string
	Text               string
}

// NewSMPPServer creates a new SMPP server
func NewSMPPServer(port int, userPort int, systemID, password string) *SMPPServer {
    server := &SMPPServer{
        sessions:        make(map[string]*Session),
        userConnections: make(map[string]net.Conn),
        port:            port,
        userPort:        userPort,
        systemID:        systemID,
        password:        password,
        trackedMessages: make(map[string]*TrackedMessage),
        ussdSessions:    make(map[string]*UssdSession),
        sessionCounter:  1000, // Start from 1000 for better readability
    }

    // Start delivery receipt processor
    go server.processDeliveryReceipts()

    return server
}

// --- New function to load configuration ---
// LoadConfig reads the server configuration from a JSON file
func LoadConfig(filePath string) (*ServerConfig, error) {
	file, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file %s: %w", filePath, err)
	}

	var config ServerConfig
	err = json.Unmarshal(file, &config)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal config JSON from %s: %w", filePath, err)
	}

	return &config, nil
}

// Start the SMPP server
func (s *SMPPServer) Start() error {
    var err error
    
    // Create TCP listener with proper options
    tcpAddr, err := net.ResolveTCPAddr("tcp", fmt.Sprintf(":%d", s.port))
    if err != nil {
        return fmt.Errorf("failed to resolve TCP address: %w", err)
    }
    
    tcpListener, err := net.ListenTCP("tcp", tcpAddr)
    if err != nil {
        return fmt.Errorf("failed to start SMPP listener: %w", err)
    }
    s.listener = tcpListener
    
    log.Printf("SMPP Server started on port %d", s.port)

    // Start user listener
    userTcpAddr, err := net.ResolveTCPAddr("tcp", fmt.Sprintf(":%d", s.userPort))
    if err != nil {
        return fmt.Errorf("failed to resolve user TCP address: %w", err)
    }
    
    userTcpListener, err := net.ListenTCP("tcp", userTcpAddr)
    if err != nil {
        return fmt.Errorf("failed to start user listener: %w", err)
    }
    s.userListener = userTcpListener
    
    log.Printf("User Simulator Listener started on port %d", s.userPort)

    // Start cleanup goroutine
    go s.cleanupSessions()

    // Accept SMPP connections
    go func() {
        for {
            conn, err := s.listener.Accept()
            if err != nil {
                log.Printf("Error accepting SMPP connection: %v", err)
                continue
            }
            go s.handleConnection(conn)
        }
    }()

    // Accept User connections
    for {
        conn, err := s.userListener.Accept()
        if err != nil {
            log.Printf("Error accepting User connection: %v", err)
            continue
        }
        go s.handleUserConnection(conn)
    }
}


// Generate or get existing session ID for USSD
func (s *SMPPServer) getOrCreateUSSDSession(msisdn, systemID string) *UssdSession {
    s.sessionMutex.Lock()
    defer s.sessionMutex.Unlock()

    // Check if session already exists
    if session, exists := s.ussdSessions[msisdn]; exists {
        // Check if session has expired
        if time.Now().Before(session.ExpiryTime) {
            // Session is still valid, extend expiry time
            session.LastActive = time.Now()
            session.ExpiryTime = time.Now().Add(10 * time.Minute) // Extend for 10 minutes
            session.SystemID = systemID // Update system ID in case it changed
            log.Printf("[USSD SESSION] Extended session %s for user %s, expires at %v", 
                session.SessionID, msisdn, session.ExpiryTime)
            return session
        } else {
            // Session expired, remove it
            log.Printf("[USSD SESSION] Session %s for user %s expired, creating new session", 
                session.SessionID, msisdn)
            delete(s.ussdSessions, msisdn)
        }
    }

    // Create new session
    s.sessionCounter++
    sessionID := fmt.Sprintf("USSD_%d", s.sessionCounter)
    
    newSession := &UssdSession{
        UserAddr:   msisdn,
        SystemID:   systemID,
        LastActive: time.Now(),
        SessionID:  sessionID,
        ExpiryTime: time.Now().Add(10 * time.Minute), // 10 minutes expiry
        State:      "active",
        CreatedAt:  time.Now(),
    }

    s.ussdSessions[msisdn] = newSession
    log.Printf("[USSD SESSION] Created new session %s for user %s, expires at %v", 
        sessionID, msisdn, newSession.ExpiryTime)

    return newSession
}

// Handle incoming connections
func (s *SMPPServer) handleConnection(conn net.Conn) {
    defer conn.Close()

    // Set TCP socket options to reduce fragmentation
    if tcpConn, ok := conn.(*net.TCPConn); ok {
        tcpConn.SetNoDelay(true)  // Disable Nagle's algorithm
        tcpConn.SetKeepAlive(true)
        tcpConn.SetKeepAlivePeriod(30 * time.Second)
    }

    session := &Session{
        conn:         conn,
        lastActivity: time.Now(),
    }

    log.Printf("New connection from %s", conn.RemoteAddr())

    for {
        pdu, err := s.readPDU(conn)
        if err != nil {
            if errors.Is(err, io.EOF) {
                log.Printf("Client %s disconnected gracefully (EOF)", conn.RemoteAddr())
            } else {
                log.Printf("Error reading PDU from %s: %v", conn.RemoteAddr(), err)
            }
            break
        }

        session.lastActivity = time.Now()

        err = s.handlePDU(session, pdu)
        if err != nil {
            log.Printf("Error handling PDU from %s: %v", conn.RemoteAddr(), err)
            break
        }
    }

    // Clean up session
    s.mutex.Lock()
    if session.systemID != "" {
        delete(s.sessions, session.systemID)
    }
    s.mutex.Unlock()

    log.Printf("Connection closed for %s", conn.RemoteAddr())
}

// New function to handle connections from the "user simulator"
func (s *SMPPServer) handleUserConnection(conn net.Conn) {
	userAddr := conn.RemoteAddr().String()
	log.Printf("New user connection from %s", userAddr)

	s.mutex.Lock()
	s.userConnections[userAddr] = conn // <--- Store the connection
	s.mutex.Unlock()

	defer func() {
		s.mutex.Lock()
		delete(s.userConnections, userAddr) // <--- Remove connection on close
		s.mutex.Unlock()
		conn.Close()
		log.Printf("User connection closed for %s", userAddr)
	}()

	reader := bufio.NewReader(conn)
	for {
		message, err := reader.ReadString('\n') // Reads until newline
		if err != nil {
			if errors.Is(err, io.EOF) {
				log.Printf("User %s disconnected gracefully (EOF)", conn.RemoteAddr())
			} else {
				log.Printf("Error reading from user %s: %v", conn.RemoteAddr(), err)
			}
			break
		}

		// Trim newline and process the user message
		message = strings.TrimSpace(message)
		log.Printf("Received raw user message from %s: '%s'", conn.RemoteAddr(), message) // Log raw message

		s.processUserMessage(message)
	}
	log.Printf("User connection closed for %s", conn.RemoteAddr())
}

// SendToUser attempts to send a text message to one or more connected user simulators.
// In a real system, this would involve routing to a specific user.
func (s *SMPPServer) SendToUser(destAddr string, message string) {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	if len(s.userConnections) == 0 {
		log.Printf("No user simulators connected to deliver message to %s: '%s'", destAddr, message)
		return
	}

	log.Printf("Attempting to deliver message to user %s: '%s' via connected user simulators.", destAddr, message)
	for userAddr, conn := range s.userConnections {
		// Using a simple protocol: FROM_SMSC|DEST_ADDR|TYPE|MESSAGE
		fullMessage := fmt.Sprintf("FROM_SMSC|%s|SMS|%s\n", destAddr, message)
		_, err := conn.Write([]byte(fullMessage))
		if err != nil {
			log.Printf("Error sending message to user simulator %s: %v", userAddr, err)
		} else {
			log.Printf("Successfully sent message to user simulator %s (for %s)", userAddr, destAddr)
		}
	}
}

// processUserMessage parses the user's text message and forwards it to an SMPP client
// Format: FROM|TO|TYPE|MESSAGE (e.g., +12345|98765|SMS|Hello!)
// Update processUserMessage to use session management
func (s *SMPPServer) processUserMessage(userMessage string) {
    parts := strings.SplitN(userMessage, "|", 4) // Split into 4 parts
    if len(parts) != 4 {
        log.Printf("Invalid user message format: '%s'. Expected FROM|TO|TYPE|MESSAGE", userMessage)
        return
    }

    fromAddr := parts[0]
    toAddr := parts[1]
    msgType := strings.ToUpper(parts[2]) // SMS or USSD
    text := parts[3]

    isUSSD := false
    esmClass := byte(ESM_DEFAULT)

    // Find a bound SMPP client (ESME) to deliver the message to.
    var targetSystemID string
    s.mutex.RLock()
    for sysID, sess := range s.sessions {
        if sess.bound && (sess.bindType == BIND_TRANSCEIVER || sess.bindType == BIND_RECEIVER) {
            targetSystemID = sysID // Found a suitable client
            break
        }
    }
    s.mutex.RUnlock()

    if targetSystemID == "" {
        log.Printf("No bound SMPP client found to deliver message from user %s to %s. Message: %s", fromAddr, toAddr, text)
        return
    }

    if msgType == "USSD" {
        isUSSD = true
        esmClass = ESM_USSD
        // Get or create USSD session
        ussdSession := s.getOrCreateUSSDSession(fromAddr, targetSystemID)
        log.Printf("[USSD] Processing USSD message from %s using session %s", fromAddr, ussdSession.SessionID)
    }

    // Create a DeliverSM message for the SMPP client
    msgToClient := &Message{
        SourceAddr:   fromAddr,
        DestAddr:     toAddr,
        ShortMessage: text,
        DataCoding:   DC_DEFAULT,
        ESMClass:     esmClass,
        IsUSSD:       isUSSD,
    }

    err := s.SendDeliverSM(targetSystemID, msgToClient)
    if err != nil {
        log.Printf("Failed to deliver %s from user %s to SMPP client %s (for %s): %v",
            msgType, fromAddr, targetSystemID, toAddr, err)
    } else {
        log.Printf("Delivered %s from user %s to SMPP client %s (for %s)",
            msgType, fromAddr, targetSystemID, toAddr)
    }
}

// Read PDU from connection
func (s *SMPPServer) readPDU(conn net.Conn) (*PDU, error) {
    // Read header first (16 bytes) - ensure complete read
    headerBuf := make([]byte, 16)
    totalRead := 0
    for totalRead < 16 {
        n, err := conn.Read(headerBuf[totalRead:])
        if err != nil {
            return nil, fmt.Errorf("failed to read PDU header: %v", err)
        }
        totalRead += n
    }

    var header PDUHeader
    buf := bytes.NewReader(headerBuf)
    if err := binary.Read(buf, binary.BigEndian, &header); err != nil {
        return nil, fmt.Errorf("failed to parse PDU header: %v", err)
    }

    // Validate command length
    if header.CommandLength < 16 || header.CommandLength > 65535 {
        return nil, fmt.Errorf("invalid command length: %d", header.CommandLength)
    }

    // Read body if present - ensure complete read
    bodyLen := header.CommandLength - 16
    var body []byte
    if bodyLen > 0 {
        body = make([]byte, bodyLen)
        totalRead := 0
        for totalRead < int(bodyLen) {
            n, err := conn.Read(body[totalRead:])
            if err != nil {
                return nil, fmt.Errorf("failed to read PDU body: %v", err)
            }
            totalRead += n
        }
    }

    log.Printf("[DEBUG] Successfully read PDU: header=%d bytes, body=%d bytes", 16, bodyLen)
    
    return &PDU{
        Header: header,
        Body:   body,
    }, nil
}

// Handle PDU based on command ID
func (s *SMPPServer) handlePDU(session *Session, pdu *PDU) error {
	switch pdu.Header.CommandID {
	case BIND_RECEIVER, BIND_TRANSMITTER, BIND_TRANSCEIVER:
		return s.handleBind(session, pdu)
	case UNBIND:
		return s.handleUnbind(session, pdu)
	case SUBMIT_SM:
		return s.handleSubmitSM(session, pdu)
	case DELIVER_SM_RESP:
		return s.handleDeliverSMResp(session, pdu)
	case ENQUIRE_LINK:
		return s.handleEnquireLink(session, pdu)
	default:
		return s.sendGenericNack(session, pdu.Header.SequenceNo, ESME_RINVCMDID)
	}
}

// Handle bind operations
func (s *SMPPServer) handleBind(session *Session, pdu *PDU) error {
    if session.bound {
        return s.sendBindResp(session, pdu, ESME_RALYBND, session.systemID)
    }

    // Parse bind PDU
    body := pdu.Body
    offset := 0

    // Extract system_id (C-string)
    systemID, offset := s.extractCString(body, offset)
    
    // Extract password (C-string)
    password, offset := s.extractCString(body, offset)
    
    // Extract system_type (C-string) - usually empty
    systemType, offset := s.extractCString(body, offset)
    
    // Validate we have enough bytes for remaining fields
    if offset+4 > len(body) {
        log.Printf("[ERROR] Bind PDU too short: offset=%d, body_len=%d", offset, len(body))
        return s.sendBindResp(session, pdu, ESME_RINVMSGLEN, systemID)
    }
    
    // Extract interface_version (1 byte)
    interfaceVersion := body[offset]
    offset++
    
    // Extract addr_ton (1 byte)
    addrTon := body[offset]
    offset++
    
    // Extract addr_npi (1 byte)
    addrNpi := body[offset]
    offset++
    
    // Extract address_range (C-string)
    addressRange := ""
    if offset < len(body) {
        addressRange, offset = s.extractCString(body, offset)
    }

    // Debug log the parsed values
    log.Printf("[DEBUG] BIND parsed: system_id=%q, password=%q, system_type=%q, interface_version=0x%02X, addr_ton=%d, addr_npi=%d, address_range=%q",
        systemID, password, systemType, interfaceVersion, addrTon, addrNpi, addressRange)

    // Validate credentials
    if systemID != s.systemID || password != s.password {
        if systemID != s.systemID {
            return s.sendBindResp(session, pdu, ESME_RINVSYSID, systemID)
        }
        return s.sendBindResp(session, pdu, ESME_RINVPASWD, systemID)
    }

    // Validate interface version (optional, but good practice)
    if interfaceVersion != 0x34 { // SMPP 3.4
        log.Printf("[WARNING] Unsupported interface version: 0x%02X, expected 0x34", interfaceVersion)
        // Continue anyway, but log the warning
    }

    // Update session
    session.systemID = systemID
    session.password = password
    session.bound = true
    session.bindType = pdu.Header.CommandID

    // Store session
    s.mutex.Lock()
    s.sessions[systemID] = session
    s.mutex.Unlock()

    log.Printf("Bound session for system_id: %s", systemID)

    return s.sendBindResp(session, pdu, ESME_ROK, systemID)
}

// Handle unbind
func (s *SMPPServer) handleUnbind(session *Session, pdu *PDU) error {
	session.bound = false

	// Send unbind response
	respPDU := &PDU{
		Header: PDUHeader{
			CommandLength: 16,
			CommandID:     UNBIND_RESP,
			CommandStatus: ESME_ROK,
			SequenceNo:    pdu.Header.SequenceNo,
		},
	}

	s.debugPDUStructure(pdu)
	return s.sendPDU(session, respPDU)
}

// Handle submit_sm (incoming SMS/USSD)
func (s *SMPPServer) handleSubmitSM(session *Session, pdu *PDU) error {
	if !session.bound {
		return s.sendSubmitSMResp(session, pdu, ESME_RINVBNDSTS, "")
	}

	// Parse submit_sm PDU
	msg, err := s.parseSubmitSM(pdu.Body)
	if err != nil {
		return s.sendSubmitSMResp(session, pdu, ESME_RINVMSGLEN, "")
	}

	// Process the message
	messageID := s.processMessage(session, msg)

	log.Printf("Received %s from %s to %s: %s",
		map[bool]string{true: "USSD", false: "SMS"}[msg.IsUSSD],
		msg.SourceAddr, msg.DestAddr, msg.ShortMessage)

	// Send response
	return s.sendSubmitSMResp(session, pdu, ESME_ROK, messageID)
}

// Handle deliver_sm_resp
func (s *SMPPServer) handleDeliverSMResp(session *Session, pdu *PDU) error {
	// Just acknowledge the response
	log.Printf("Received deliver_sm_resp from %s", session.systemID)
	return nil
}

// Handle enquire_link
func (s *SMPPServer) handleEnquireLink(session *Session, pdu *PDU) error {
	respPDU := &PDU{
		Header: PDUHeader{
			CommandLength: 16,
			CommandID:     ENQUIRE_LINK_RESP,
			CommandStatus: ESME_ROK,
			SequenceNo:    pdu.Header.SequenceNo,
		},
	}

	s.debugPDUStructure(pdu)
	return s.sendPDU(session, respPDU)
}

// Send bind response
func (s *SMPPServer) sendBindResp(session *Session, pdu *PDU, status uint32, systemID string) error {
    var respID uint32
    switch pdu.Header.CommandID {
    case BIND_RECEIVER:
        respID = BIND_RECEIVER_RESP
    case BIND_TRANSMITTER:
        respID = BIND_TRANSMITTER_RESP
    case BIND_TRANSCEIVER:
        respID = BIND_TRANSCEIVER_RESP
    default:
        respID = GENERIC_NACK
    }

    var body []byte
    if status == ESME_ROK {
        // For successful bind, include system_id in response
        body = append(body, []byte(systemID)...)
        body = append(body, 0) // null terminator
        
        // Optionally add TLV parameters for additional info
        // For now, just send the system_id
    } else {
        // For error responses, system_id can be empty
        body = append(body, 0) // just null terminator
    }

    respPDU := &PDU{
        Header: PDUHeader{
            CommandLength: uint32(16 + len(body)),
            CommandID:     respID,
            CommandStatus: status,
            SequenceNo:    pdu.Header.SequenceNo,
        },
        Body: body,
    }

    log.Printf("[DEBUG] Sending bind response: status=0x%08X, system_id=%q", status, systemID)
    s.debugPDUStructure(respPDU)
    return s.sendPDU(session, respPDU)
}

// Send submit_sm response
func (s *SMPPServer) sendSubmitSMResp(session *Session, pdu *PDU, status uint32, messageID string) error {
	body := []byte(messageID)
	body = append(body, 0) // null terminator

	respPDU := &PDU{
		Header: PDUHeader{
			CommandLength: uint32(16 + len(body)),
			CommandID:     SUBMIT_SM_RESP,
			CommandStatus: status,
			SequenceNo:    pdu.Header.SequenceNo,
		},
		Body: body,
	}

	s.debugPDUStructure(pdu)
	return s.sendPDU(session, respPDU)
}

// Send generic NACK
func (s *SMPPServer) sendGenericNack(session *Session, seqNo uint32, status uint32) error {
	respPDU := &PDU{
		Header: PDUHeader{
			CommandLength: 16,
			CommandID:     GENERIC_NACK,
			CommandStatus: status,
			SequenceNo:    seqNo,
		},
	}

	return s.sendPDU(session, respPDU)
}

// Send deliver_sm (outgoing SMS/USSD)
func (s *SMPPServer) SendDeliverSM(systemID string, msg *Message) error {
    // For USSD, keep ESMClass as default and use TLV for indication
    if msg.IsUSSD {
        msg.ESMClass = ESM_DEFAULT // 0x00 = no special ESM flags
    }
    
    s.mutex.RLock()
    session, exists := s.sessions[systemID]
    s.mutex.RUnlock()

    if !exists || !session.bound {
        return fmt.Errorf("session not found or not bound: %s", systemID)
    }

    session.mutex.Lock()
    session.sequenceNo++
    seqNo := session.sequenceNo
    session.mutex.Unlock()

    // Generate message_id - use consistent session ID for USSD
    var messageID string
    if msg.IsUSSD {
        // For USSD, use the session ID from the USSD session
        s.sessionMutex.RLock()
        if ussdSession, exists := s.ussdSessions[msg.SourceAddr]; exists {
            messageID = ussdSession.SessionID
        } else if ussdSession, exists := s.ussdSessions[msg.DestAddr]; exists {
            messageID = ussdSession.SessionID
        } else {
            // Fallback - create new session if none exists
            var userAddr string
            if msg.SourceAddr != "" {
                userAddr = msg.SourceAddr
            } else {
                userAddr = msg.DestAddr
            }
            s.sessionMutex.RUnlock()
            ussdSession := s.getOrCreateUSSDSession(userAddr, systemID)
            messageID = ussdSession.SessionID
            s.sessionMutex.RLock()
        }
        s.sessionMutex.RUnlock()
    } else {
        // For SMS, use timestamp-based message ID
        messageID = fmt.Sprintf("MSG%d%d", time.Now().Unix(), time.Now().Nanosecond()%1000)
    }

    // Build deliver_sm body first
    body := s.buildDeliverSMBody(msg, messageID)

    // Build TLVs
    var tlvs []byte
    
    // Always add a message_id TLV (tag 0x001E)
    messageIDBytes := []byte(messageID)
    messageIDBytes = append(messageIDBytes, 0) // null-terminated
    tlvs = append(tlvs, buildTLV(0x001E, messageIDBytes)...)

    if msg.IsUSSD {
        // TLV 0x1401 (ncs_id) - this indicates USSD
        ncsBytes := []byte("ussdc")
        ncsBytes = append(ncsBytes, 0) // null-terminated
        tlvs = append(tlvs, buildTLV(0x1401, ncsBytes)...)
        
        // TLV 0x1402 (for compatibility)
        ncsBytes2 := []byte("ussdc")
        ncsBytes2 = append(ncsBytes2, 0) // null-terminated
        tlvs = append(tlvs, buildTLV(0x1402, ncsBytes2)...)
        
        // Optional: Add USSD service operation TLV
        ussdOpBytes := []byte{0x01} // MO_INIT or appropriate value
        tlvs = append(tlvs, buildTLV(0x0501, ussdOpBytes)...)
    }

    // Combine body and TLVs
    fullBody := append(body, tlvs...)

    // Calculate correct command length (16 byte header + body + TLVs)
    commandLength := uint32(16 + len(fullBody))

    // Create PDU
    pdu := &PDU{
        Header: PDUHeader{
            CommandLength: commandLength,
            CommandID:     DELIVER_SM,
            CommandStatus: ESME_ROK,
            SequenceNo:    seqNo,
        },
        Body: fullBody,
    }

    // Print debug info
    ncsType := "smsc"
    if msg.IsUSSD {
        ncsType = "ussdc"
    }
    
    log.Printf("[DEBUG] DELIVER_SM: ncsType=%s, message_id=%s, src=%s, dst=%s, text=%q", 
        ncsType, messageID, msg.SourceAddr, msg.DestAddr, msg.ShortMessage)
    log.Printf("[DEBUG] PDU Length: %d, Body Length: %d, TLV Length: %d", 
        commandLength, len(body), len(tlvs))
    
    rawPDU := pdu.Marshal()
    log.Printf("[DEBUG] Raw DELIVER_SM PDU: % X", rawPDU)
    
	s.debugPDUStructure(pdu)
    return s.sendPDU(session, pdu)
}

// Marshal serializes the PDU into a byte slice (header + body)
func (p *PDU) Marshal() []byte {
    buf := new(bytes.Buffer)
    
    // Validate command length
    expectedLength := 16 + len(p.Body)
    if p.Header.CommandLength != uint32(expectedLength) {
        log.Printf("[WARNING] PDU command length mismatch: header says %d, actual is %d", 
            p.Header.CommandLength, expectedLength)
        // Fix the length
        p.Header.CommandLength = uint32(expectedLength)
    }
    
    // Write header fields in big endian order
    binary.Write(buf, binary.BigEndian, p.Header.CommandLength)
    binary.Write(buf, binary.BigEndian, p.Header.CommandID)
    binary.Write(buf, binary.BigEndian, p.Header.CommandStatus)
    binary.Write(buf, binary.BigEndian, p.Header.SequenceNo)
    
    // Write body if present
    if p.Body != nil {
        buf.Write(p.Body)
    }
    
    return buf.Bytes()
}


// Add this function for debugging
func (s *SMPPServer) debugPDUStructure(pdu *PDU) {
    log.Printf("[DEBUG] PDU Structure:")
    log.Printf("  Command Length: %d (0x%08X)", pdu.Header.CommandLength, pdu.Header.CommandLength)
    log.Printf("  Command ID: %d (0x%08X)", pdu.Header.CommandID, pdu.Header.CommandID)
    log.Printf("  Command Status: %d (0x%08X)", pdu.Header.CommandStatus, pdu.Header.CommandStatus)
    log.Printf("  Sequence No: %d (0x%08X)", pdu.Header.SequenceNo, pdu.Header.SequenceNo)
    log.Printf("  Body Length: %d", len(pdu.Body))
    
    // Flag potentially problematic PDUs
    if pdu.Header.CommandLength > 1024 {
        log.Printf("  [WARNING] Large PDU detected: %d bytes", pdu.Header.CommandLength)
    }
    
    if len(pdu.Body) > 0 {
        displayLen := min(100, len(pdu.Body))
        log.Printf("  Body (first %d bytes): % X", displayLen, pdu.Body[:displayLen])
    }
}

func min(a, b int) int {
    if a < b {
        return a
    }
    return b
}

// buildTLV constructs a TLV (Tag-Length-Value) for SMPP
func buildTLV(tag uint16, value []byte) []byte {
    // Ensure value is not nil
    if value == nil {
        value = []byte{}
    }
    
    tlv := make([]byte, 4+len(value))
    // Tag (2 bytes, big endian)
    binary.BigEndian.PutUint16(tlv[0:2], tag)
    // Length (2 bytes, big endian)
    binary.BigEndian.PutUint16(tlv[2:4], uint16(len(value)))
    // Value
    if len(value) > 0 {
        copy(tlv[4:], value)
    }
    return tlv
}

// Build deliver_sm body
func (s *SMPPServer) buildDeliverSMBody(msg *Message, messageID string) []byte {
    var body []byte

    // service_type (C-string - null terminated)
    body = append(body, 0)

    // source_addr_ton, source_addr_npi (1 byte each)
    body = append(body, 0, 0)

    // source_addr (C-string)
    sourceAddr := msg.SourceAddr
    if sourceAddr == "" {
        sourceAddr = "UNKNOWN"
    }
    body = append(body, []byte(sourceAddr)...)
    body = append(body, 0) // null terminator

    // dest_addr_ton, dest_addr_npi (1 byte each)
    body = append(body, 0, 0)

    // destination_addr (C-string)
    destAddr := msg.DestAddr
    if destAddr == "" {
        destAddr = "UNKNOWN"
    }
    body = append(body, []byte(destAddr)...)
    body = append(body, 0) // null terminator

    // esm_class (1 byte)
    body = append(body, msg.ESMClass)

    // protocol_id (1 byte)
    body = append(body, 0)

    // priority_flag (1 byte)
    body = append(body, 0)

    // schedule_delivery_time (C-string - empty, so just null)
    body = append(body, 0)

    // validity_period (C-string - empty, so just null)
    body = append(body, 0)

    // registered_delivery (1 byte)
    body = append(body, msg.RegisteredDelivery)

    // replace_if_present_flag (1 byte)
    body = append(body, 0)

    // data_coding (1 byte)
    body = append(body, msg.DataCoding)

    // sm_default_msg_id (1 byte)
    body = append(body, 0)

    // Prepare short_message
    msgText := msg.ShortMessage
    if msgText == "" {
        msgText = "" // Allow empty messages
    }
    
    msgBytes := []byte(msgText)
    
    // SMPP constraint: short_message max length is 254 bytes
    if len(msgBytes) > 254 {
        msgBytes = msgBytes[:254]
    }
    
    // sm_length (1 byte) - length of short_message
    body = append(body, byte(len(msgBytes)))
    
    // short_message (variable length)
    if len(msgBytes) > 0 {
        body = append(body, msgBytes...)
    }

    return body
}

func (s *SMPPServer) parseSubmitSM(body []byte) (*Message, error) {
    offset := 0

    // service_type (C-string)
    _, offset = s.extractCString(body, offset)

    // source_addr_ton
    if offset >= len(body) {
        return nil, fmt.Errorf("invalid PDU format (source_addr_ton)")
    }
    sourceAddrTON := body[offset]
    offset++

    // source_addr_npi
    if offset >= len(body) {
        return nil, fmt.Errorf("invalid PDU format (source_addr_npi)")
    }
    sourceAddrNPI := body[offset]
    offset++

    // source_addr (C-string)
    sourceAddr, offset := s.extractCString(body, offset)

    // dest_addr_ton
    if offset >= len(body) {
        return nil, fmt.Errorf("invalid PDU format (dest_addr_ton)")
    }
    destAddrTON := body[offset]
    offset++

    // dest_addr_npi
    if offset >= len(body) {
        return nil, fmt.Errorf("invalid PDU format (dest_addr_npi)")
    }
    destAddrNPI := body[offset]
    offset++

    // destination_addr (C-string)
    destAddr, offset := s.extractCString(body, offset)

    // esm_class
    if offset >= len(body) {
        return nil, fmt.Errorf("invalid PDU format (esm_class)")
    }
    esmClass := body[offset]
    offset++

    // protocol_id
    offset++
    // priority_flag
    offset++

    // schedule_delivery_time (C-string)
    _, offset = s.extractCString(body, offset)
    // validity_period (C-string)
    _, offset = s.extractCString(body, offset)

    // registered_delivery
    if offset >= len(body) {
        return nil, fmt.Errorf("invalid PDU format (registered_delivery)")
    }
    registeredDelivery := body[offset]
    offset++

    // replace_if_present_flag
    offset++

    // data_coding
    if offset >= len(body) {
        return nil, fmt.Errorf("invalid PDU format (data_coding)")
    }
    dataCoding := body[offset]
    offset++

    // sm_default_msg_id
    offset++

        // sm_length
    if offset >= len(body) {
        return nil, fmt.Errorf("invalid PDU format (sm_length)")
    }
    smLength := int(body[offset])
    offset++

    // short_message
    if offset+smLength > len(body) {
        return nil, fmt.Errorf("invalid message length: offset=%d smLength=%d bodyLen=%d", offset, smLength, len(body))
    }
    shortMessage := ""
    if smLength > 0 {
        shortMessage = string(body[offset : offset+smLength])
    }
    offset += smLength

    // If short_message is empty, check for message_payload TLV (tag 0x0424)
    if shortMessage == "" && offset < len(body) {
        i := offset
        for i+4 <= len(body) {
            tag := binary.BigEndian.Uint16(body[i : i+2])
            length := binary.BigEndian.Uint16(body[i+2 : i+4])
            if int(i+4+int(length)) > len(body) {
                break
            }
            if tag == 0x0424 { // message_payload
                payload := body[i+4 : i+4+int(length)]
                shortMessage = string(payload)
                break
            }
            i += 4 + int(length)
        }
    }

    // Debug log
    log.Printf("[DEBUG] SUBMIT_SM: src_ton=%d src_npi=%d dst_ton=%d dst_npi=%d src=%q dst=%q esm_class=0x%02X data_coding=0x%02X sm_length=%d short_message=%q",
        sourceAddrTON, sourceAddrNPI, destAddrTON, destAddrNPI, sourceAddr, destAddr, esmClass, dataCoding, smLength, shortMessage)

    return &Message{
        SourceAddr:         sourceAddr,
        DestAddr:           destAddr,
        ShortMessage:       shortMessage,
        DataCoding:         dataCoding,
        ESMClass:           esmClass,
        IsUSSD:             (esmClass & ESM_USSD) != 0,
        RegisteredDelivery: registeredDelivery,
    }, nil
}

// Extract C string from byte array
func (s *SMPPServer) extractCString(data []byte, offset int) (string, int) {
    if offset >= len(data) {
        return "", offset
    }
    
    start := offset
    for offset < len(data) && data[offset] != 0 {
        offset++
    }
    
    result := ""
    if offset > start {
        result = string(data[start:offset])
    }
    
    if offset < len(data) {
        offset++ // skip null terminator
    }
    
    return result, offset
}

// Send PDU to session
func (s *SMPPServer) sendPDU(session *Session, pdu *PDU) error {
    // Create buffer for PDU
    buf := new(bytes.Buffer)

    // Write header fields in correct order and size
    binary.Write(buf, binary.BigEndian, pdu.Header.CommandLength)
    binary.Write(buf, binary.BigEndian, pdu.Header.CommandID)
    binary.Write(buf, binary.BigEndian, pdu.Header.CommandStatus)
    binary.Write(buf, binary.BigEndian, pdu.Header.SequenceNo)

    // Write body if present
    if pdu.Body != nil {
        buf.Write(pdu.Body)
    }

    // Get the complete PDU bytes
    pduBytes := buf.Bytes()
    
    // Validate PDU size
    if len(pduBytes) != int(pdu.Header.CommandLength) {
        return fmt.Errorf("PDU size mismatch: expected %d, got %d", pdu.Header.CommandLength, len(pduBytes))
    }

    // Send to connection with proper error handling
    session.mutex.Lock()
    defer session.mutex.Unlock()

    // Ensure complete write
    totalWritten := 0
    for totalWritten < len(pduBytes) {
        written, err := session.conn.Write(pduBytes[totalWritten:])
        if err != nil {
            return fmt.Errorf("failed to write PDU: %v", err)
        }
        totalWritten += written
    }

    log.Printf("[DEBUG] Successfully sent PDU: %d bytes", len(pduBytes))
    return nil
}

// processMessage handles an incoming SubmitSM from an SMPP client
func (s *SMPPServer) processMessage(session *Session, msg *Message) string {
	// Generate message ID
	messageID := fmt.Sprintf("MSG%d%d", time.Now().Unix(), time.Now().Nanosecond()%1000)

	// Track message for delivery receipts if requested
	if msg.RegisteredDelivery != RD_NONE && !msg.IsUSSD {
		trackedMsg := &TrackedMessage{
			MessageID:          messageID,
			SourceAddr:         msg.SourceAddr,
			DestAddr:           msg.DestAddr,
			SystemID:           session.systemID,
			SubmitTime:         time.Now(),
			RegisteredDelivery: msg.RegisteredDelivery,
			Status:             MSG_STATE_ACCEPTED,
			Text:               msg.ShortMessage,
		}

		s.messageMutex.Lock()
		s.trackedMessages[messageID] = trackedMsg
		s.messageMutex.Unlock()

		log.Printf("Tracking message %s for delivery receipt", messageID)
	}

	// --- NEW LOGIC HERE: Simulate message delivery to a mobile user ---
	msgType := map[bool]string{true: "USSD", false: "SMS"}[msg.IsUSSD]
	log.Printf("📱 Received %s from SMPP client %s (from %s to %s). Simulating delivery to mobile user.",
		msgType, session.systemID, msg.SourceAddr, msg.DestAddr)
	log.Printf("   User %s received: '%s'", msg.DestAddr, msg.ShortMessage)

	// --- IMPORTANT: Call SendToUser here for messages coming FROM SMPP Client ---
	if !msg.IsUSSD { // Typically, only SMS would be "delivered" to a user this way
		s.SendToUser(msg.DestAddr, msg.ShortMessage) // <--- THIS IS THE KEY CHANGE
	}
	// --------------------------------------------------------------------------

	// ------------------------------------------------------------------

	if msg.IsUSSD {
		// Handle USSD request (auto-reply for USSD from client)
		s.handleUSSDRequest(session, msg)
	} else {
		// Handle SMS (auto-reply for SMS from client)
		s.handleSMSRequest(session, msg, messageID)
	}

	return messageID
}

// Handle USSD request
func (s *SMPPServer) handleUSSDRequest(session *Session, msg *Message) {
    // Get USSD session
    s.sessionMutex.RLock()
    ussdSession, exists := s.ussdSessions[msg.SourceAddr]
    s.sessionMutex.RUnlock()

    if exists {
        log.Printf("[USSD SESSION] User: %s, SessionID: %s, SystemID: %s, LastActive: %v", 
            ussdSession.UserAddr, ussdSession.SessionID, ussdSession.SystemID, ussdSession.LastActive)
        
        // Update session activity
        s.sessionMutex.Lock()
        ussdSession.LastActive = time.Now()
        ussdSession.ExpiryTime = time.Now().Add(10 * time.Minute)
        s.sessionMutex.Unlock()
    }

    // Example USSD menu handling (you can customize this)
    var response string
    switch msg.ShortMessage {
    case "*123#":
        response = "Welcome to USSD Service\n1. Balance\n2. Recharge\n3. Help"
    case "1":
        response = "Your balance is $10.50"
    case "2":
        response = "Enter recharge amount:"
    case "3":
        response = "Call 123 for help"
    default:
        response = "Invalid option. Please try again."
    }

    // Send USSD response
    responseMsg := &Message{
        SourceAddr:   msg.DestAddr,
        DestAddr:     msg.SourceAddr,
        ShortMessage: response,
        DataCoding:   DC_DEFAULT,
        ESMClass:     ESM_USSD,
        IsUSSD:       true,
    }

    go func() {
        time.Sleep(100 * time.Millisecond) // Small delay
        s.SendDeliverSM(session.systemID, responseMsg)
    }()
}

func (s *SMPPServer) cleanupUssdSessions() {
    ticker := time.NewTicker(1 * time.Minute) // Check every minute
    defer ticker.Stop()
    
    for range ticker.C {
        s.sessionMutex.Lock()
        now := time.Now()
        var expiredSessions []string
        
        for msisdn, session := range s.ussdSessions {
            if now.After(session.ExpiryTime) {
                expiredSessions = append(expiredSessions, msisdn)
            }
        }
        
        // Remove expired sessions
        for _, msisdn := range expiredSessions {
            session := s.ussdSessions[msisdn]
            log.Printf("[USSD SESSION] Removing expired session %s for user %s (expired at %v)", 
                session.SessionID, msisdn, session.ExpiryTime)
            delete(s.ussdSessions, msisdn)
        }
        
        s.sessionMutex.Unlock()
        
        if len(expiredSessions) > 0 {
            log.Printf("[USSD SESSION] Cleaned up %d expired sessions", len(expiredSessions))
        }
    }
}

// Add a function to get session info (for debugging)
func (s *SMPPServer) GetUSSDSessionInfo(msisdn string) (*UssdSession, bool) {
    s.sessionMutex.RLock()
    defer s.sessionMutex.RUnlock()
    
    session, exists := s.ussdSessions[msisdn]
    return session, exists
}

// Add a function to manually expire a session (for testing)
func (s *SMPPServer) ExpireUSSDSession(msisdn string) bool {
    s.sessionMutex.Lock()
    defer s.sessionMutex.Unlock()
    
    if session, exists := s.ussdSessions[msisdn]; exists {
        session.ExpiryTime = time.Now().Add(-1 * time.Minute) // Set to past time
        log.Printf("[USSD SESSION] Manually expired session %s for user %s", session.SessionID, msisdn)
        return true
    }
    return false
}

// Handle SMS request
func (s *SMPPServer) handleSMSRequest(session *Session, msg *Message, messageID string) {
	// Example SMS auto-reply
	if msg.ShortMessage == "PING" {
		responseMsg := &Message{
			SourceAddr:   msg.DestAddr,
			DestAddr:     msg.SourceAddr,
			ShortMessage: "PONG",
			DataCoding:   DC_DEFAULT,
			ESMClass:     ESM_DEFAULT,
			IsUSSD:       false,
		}

		go func() {
			time.Sleep(100 * time.Millisecond)
			s.SendDeliverSM(session.systemID, responseMsg)
		}()
	}

	// Simulate message delivery for tracked messages
	if msg.RegisteredDelivery != RD_NONE {
		go func() {
			// Simulate delivery delay
			time.Sleep(time.Duration(1+time.Now().UnixNano()%5) * time.Second)

			// Update message status to delivered
			s.messageMutex.Lock()
			if trackedMsg, exists := s.trackedMessages[messageID]; exists {
				trackedMsg.Status = MSG_STATE_DELIVERED
			}
			s.messageMutex.Unlock()
		}()
	}
}

// Cleanup inactive sessions
func (s *SMPPServer) cleanupSessions() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		s.mutex.Lock()
		for systemID, session := range s.sessions {
			if time.Since(session.lastActivity) > 5*time.Minute {
				log.Printf("Cleaning up inactive session: %s", systemID)
				session.conn.Close()
				delete(s.sessions, systemID)
			}
		}
		s.mutex.Unlock()
	}
}

// Process delivery receipts
func (s *SMPPServer) processDeliveryReceipts() {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		s.messageMutex.RLock()
		var receiptsToSend []*TrackedMessage

		for msgID, trackedMsg := range s.trackedMessages {
			// Check if message is delivered and receipt should be sent
			if trackedMsg.Status == MSG_STATE_DELIVERED &&
				time.Since(trackedMsg.SubmitTime) > 1*time.Second {
				receiptsToSend = append(receiptsToSend, trackedMsg)
			}

			// Clean up old messages (older than 1 hour)
			if time.Since(trackedMsg.SubmitTime) > 1*time.Hour {
				delete(s.trackedMessages, msgID)
			}
		}
		s.messageMutex.RUnlock()

		// Send delivery receipts
		for _, trackedMsg := range receiptsToSend {
			if s.shouldSendDeliveryReceipt(trackedMsg) {
				s.sendDeliveryReceipt(trackedMsg)

				// Remove from tracking after sending receipt
				s.messageMutex.Lock()
				delete(s.trackedMessages, trackedMsg.MessageID)
				s.messageMutex.Unlock()
			}
		}
	}
}

// Check if delivery receipt should be sent
func (s *SMPPServer) shouldSendDeliveryReceipt(trackedMsg *TrackedMessage) bool {
	switch trackedMsg.RegisteredDelivery {
	case RD_SUCCESS_FAILURE:
		return trackedMsg.Status == MSG_STATE_DELIVERED ||
			trackedMsg.Status == MSG_STATE_UNDELIVERABLE ||
			trackedMsg.Status == MSG_STATE_REJECTED
	case RD_FAILURE:
		return trackedMsg.Status == MSG_STATE_UNDELIVERABLE ||
			trackedMsg.Status == MSG_STATE_REJECTED
	case RD_SUCCESS:
		return trackedMsg.Status == MSG_STATE_DELIVERED
	default:
		return false
	}
}

// Send delivery receipt
func (s *SMPPServer) sendDeliveryReceipt(trackedMsg *TrackedMessage) {
	receipt := &DeliveryReceipt{
		MessageID:    trackedMsg.MessageID,
		SourceAddr:   trackedMsg.SourceAddr,
		DestAddr:     trackedMsg.DestAddr,
		MessageState: trackedMsg.Status,
		SubmitDate:   trackedMsg.SubmitTime,
		DoneDate:     time.Now(),
		ErrorCode:    "000",
		Text:         trackedMsg.Text,
	}

	receiptText := s.formatDeliveryReceipt(receipt)

	// Send as deliver_sm with delivery receipt indication
	msg := &Message{
		SourceAddr:   trackedMsg.DestAddr,
		DestAddr:     trackedMsg.SourceAddr,
		ShortMessage: receiptText,
		DataCoding:   DC_DEFAULT,
		ESMClass:     ESM_DELIVERY_RECEIPT,
		IsUSSD:       false,
	}

	err := s.SendDeliverSM(trackedMsg.SystemID, msg)
	if err != nil {
		log.Printf("Failed to send delivery receipt for message %s: %v",
			trackedMsg.MessageID, err)
	} else {
		log.Printf("Sent delivery receipt for message %s: %s",
			trackedMsg.MessageID, trackedMsg.Status)
	}
}

// Format delivery receipt message
func (s *SMPPServer) formatDeliveryReceipt(receipt *DeliveryReceipt) string {
	submitDate := receipt.SubmitDate.Format("0601021504")
	doneDate := receipt.DoneDate.Format("0601021504")

	return fmt.Sprintf("id:%s submit date:%s done date:%s stat:%s err:%s text:%s",
		receipt.MessageID,
		submitDate,
		doneDate,
		receipt.MessageState,
		receipt.ErrorCode,
		receipt.Text)
}

// Simulate message delivery failure (for testing)
func (s *SMPPServer) SimulateDeliveryFailure(messageID string) {
	s.messageMutex.Lock()
	defer s.messageMutex.Unlock()

	if trackedMsg, exists := s.trackedMessages[messageID]; exists {
		trackedMsg.Status = MSG_STATE_UNDELIVERABLE
		log.Printf("Simulated delivery failure for message %s", messageID)
	}
}

// Get tracked message status
func (s *SMPPServer) GetMessageStatus(messageID string) (string, bool) {
	s.messageMutex.RLock()
	defer s.messageMutex.RUnlock()

	if trackedMsg, exists := s.trackedMessages[messageID]; exists {
		return trackedMsg.Status, true
	}
	return "", false
}

// Stop the server
func (s *SMPPServer) Stop() error {
	if s.listener != nil {
		return s.listener.Close()
	}
	return nil
}

// Main function - example usage
func main() {
	// --- Load configuration from file ---\
	config, err := LoadConfig("server_config.json") // Changed path here

	if err != nil {
		log.Fatalf("Failed to load server configuration: %v", err)
	}

	// Create SMPP server using loaded configuration
	server := NewSMPPServer(config.Port, config.UserPort, config.SystemID, config.Password)

	// Start USSD session cleanup goroutine
    go server.cleanupUssdSessions()
	// Example: Test delivery receipt functionality
	go func() {
		time.Sleep(30 * time.Second)

		// You can simulate delivery failures for testing
		// server.SimulateDeliveryFailure("some-message-id")

		// Check message status
		// status, exists := server.GetMessageStatus("some-message-id")
		// if exists {
		//     log.Printf("Message status: %s", status)
		// }
	}()

	// Start server
	log.Fatal(server.Start())
}
