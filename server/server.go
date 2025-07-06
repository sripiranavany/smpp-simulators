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

// SMPP Server
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
	s.listener, err = net.Listen("tcp", fmt.Sprintf(":%d", s.port))
	if err != nil {
		return fmt.Errorf("failed to start SMPP listener: %w", err)
	}
	log.Printf("SMPP Server started on port %d", s.port)

	// --- NEW: Start listener for user connections ---
	s.userListener, err = net.Listen("tcp", fmt.Sprintf(":%d", s.userPort))
	if err != nil {
		return fmt.Errorf("failed to start user listener: %w", err)
	}
	log.Printf("User Simulator Listener started on port %d", s.userPort)
	// --- END NEW ---

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
			go s.handleConnection(conn) // Handles SMPP PDUs
		}
	}()

	// --- NEW: Accept User connections ---
	for {
		conn, err := s.userListener.Accept()
		if err != nil {
			log.Printf("Error accepting User connection: %v", err)
			continue
		}
		go s.handleUserConnection(conn) // Handles simple text messages from users
	}
	// --- END NEW ---
}

// Handle incoming connections
func (s *SMPPServer) handleConnection(conn net.Conn) {
	defer conn.Close()

	session := &Session{
		conn:         conn,
		lastActivity: time.Now(),
	}

	log.Printf("New connection from %s", conn.RemoteAddr())

	for {
		pdu, err := s.readPDU(conn)
		if err != nil {
			if errors.Is(err, io.EOF) {
				// Client gracefully closed the connection
				log.Printf("Client %s disconnected gracefully (EOF)", conn.RemoteAddr())
			} else {
				// This is a real error during PDU reading
				log.Printf("Error reading PDU from %s: %v", conn.RemoteAddr(), err)
			}
			break // Exit the loop whether it's EOF or another error
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
	if msgType == "USSD" {
		isUSSD = true
		esmClass = ESM_USSD
	}

	// Find a bound SMPP client (ESME) to deliver the message to.
	// For simplicity, we'll just pick the first bound TRANSCEIVER or RECEIVER.
	// In a real SMSC, you'd have sophisticated routing logic based on 'toAddr' or other criteria.
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

	// Create a DeliverSM message for the SMPP client
	msgToClient := &Message{
		SourceAddr:   fromAddr,
		DestAddr:     toAddr,
		ShortMessage: text,
		DataCoding:   DC_DEFAULT, // Default data coding for user messages
		ESMClass:     esmClass,
		IsUSSD:       isUSSD,
		// RegisteredDelivery is not applicable for incoming messages from user to client
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
	// Read header first (16 bytes)
	headerBuf := make([]byte, 16)
	_, err := conn.Read(headerBuf)
	if err != nil {
		return nil, err
	}

	var header PDUHeader
	buf := bytes.NewReader(headerBuf)
	binary.Read(buf, binary.BigEndian, &header)

	// Validate command length
	if header.CommandLength < 16 || header.CommandLength > 65535 {
		return nil, fmt.Errorf("invalid command length: %d", header.CommandLength)
	}

	// Read body if present
	bodyLen := header.CommandLength - 16
	var body []byte
	if bodyLen > 0 {
		body = make([]byte, bodyLen)
		_, err = conn.Read(body)
		if err != nil {
			return nil, err
		}
	}

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
		return s.sendBindResp(session, pdu, ESME_RALYBND)
	}

	// Parse bind PDU
	body := pdu.Body
	offset := 0

	// Extract system_id
	systemID, offset := s.extractCString(body, offset)
	password, offset := s.extractCString(body, offset)

	// Validate credentials
	if systemID != s.systemID || password != s.password {
		if systemID != s.systemID {
			return s.sendBindResp(session, pdu, ESME_RINVSYSID)
		}
		return s.sendBindResp(session, pdu, ESME_RINVPASWD)
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

	return s.sendBindResp(session, pdu, ESME_ROK)
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

	return s.sendPDU(session, respPDU)
}

// Send bind response
func (s *SMPPServer) sendBindResp(session *Session, pdu *PDU, status uint32) error {
	var respID uint32
	switch pdu.Header.CommandID {
	case BIND_RECEIVER:
		respID = BIND_RECEIVER_RESP
	case BIND_TRANSMITTER:
		respID = BIND_TRANSMITTER_RESP
	case BIND_TRANSCEIVER:
		respID = BIND_TRANSCEIVER_RESP
	}

	// Create response body with system_id
	body := []byte(s.systemID)
	body = append(body, 0) // null terminator

	respPDU := &PDU{
		Header: PDUHeader{
			CommandLength: uint32(16 + len(body)),
			CommandID:     respID,
			CommandStatus: status,
			SequenceNo:    pdu.Header.SequenceNo,
		},
		Body: body,
	}

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
	   // For USSD, set ESMClass to 0xC0 (USSD indication, no UDHI, per SMPP spec)
	   if msg.IsUSSD {
			   msg.ESMClass = 0xC0 // 0xC0 = 1100 0000: USSD indication, no UDHI
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


	   // Build deliver_sm PDU
	   var tlvs []byte
	   body := s.buildDeliverSMBody(msg)

	   // Always add a message_id TLV (tag 0x001E) for compatibility with Java clients
	   // Generate a unique message_id for each DELIVER_SM
	   messageID := fmt.Sprintf("MSG%d%d", time.Now().Unix(), time.Now().Nanosecond()%1000)
	   tag := []byte{0x00, 0x1E}
	   val := append([]byte(messageID), 0) // null-terminated
	   length := []byte{0x00, byte(len(val))}
	   tlvs = append(tlvs, tag...)
	   tlvs = append(tlvs, length...)
	   tlvs = append(tlvs, val...)

	   // Print ncs type and message_id for debug
	   ncsType := "smsc"
	   if msg.IsUSSD {
			   ncsType = "ussdc"
	   }
	   fmt.Printf("[DEBUG] DELIVER_SM: ncsType=%s, message_id=%s, src=%s, dst=%s, text=%q\n", ncsType, messageID, msg.SourceAddr, msg.DestAddr, msg.ShortMessage)

	   // If this is a USSD message, add ncs_id TLV (tag 0x14 0x01) with value "ussdc\0"
	   if msg.IsUSSD {
			   tag1 := []byte{0x14, 0x01}
			   val := append([]byte("ussdc"), 0) // null-terminated
			   length := []byte{0x00, byte(len(val))}
			   tlvs = append(tlvs, tag1...)
			   tlvs = append(tlvs, length...)
			   tlvs = append(tlvs, val...)
	   }

	   fullBody := append(body, tlvs...)

	   pdu := &PDU{
			   Header: PDUHeader{
					   CommandLength: uint32(16 + len(fullBody)),
					   CommandID:     DELIVER_SM,
					   CommandStatus: ESME_ROK,
					   SequenceNo:    seqNo,
			   },
			   Body: fullBody,
	   }

	   return s.sendPDU(session, pdu)
}

// Build deliver_sm body
func (s *SMPPServer) buildDeliverSMBody(msg *Message) []byte {
	   var body []byte

	   // service_type (null)
	   body = append(body, 0)

	   // source_addr_ton, source_addr_npi
	   body = append(body, 0, 0)

	   // source_addr
	   if msg.SourceAddr == "" {
			   msg.SourceAddr = "UNKNOWN"
	   }
	   body = append(body, []byte(msg.SourceAddr)...)
	   body = append(body, 0)

	   // dest_addr_ton, dest_addr_npi
	   body = append(body, 0, 0)

	   // destination_addr
	   if msg.DestAddr == "" {
			   msg.DestAddr = "UNKNOWN"
	   }
	   body = append(body, []byte(msg.DestAddr)...)
	   body = append(body, 0)

	   // esm_class
	   body = append(body, msg.ESMClass)

	   // protocol_id
	   body = append(body, 0)

	   // priority_flag
	   body = append(body, 0)

	   // schedule_delivery_time (null)
	   body = append(body, 0)

	   // validity_period (null)
	   body = append(body, 0)

	   // registered_delivery
	   body = append(body, 0)

	   // replace_if_present_flag
	   body = append(body, 0)

	   // data_coding
	   body = append(body, msg.DataCoding)

	   // sm_default_msg_id
	   body = append(body, 0)

	   // sm_length and short_message
	   msgText := msg.ShortMessage
	   if msgText == "" {
			   msgText = " " // Ensure at least one space if empty
	   }
	   msgBytes := []byte(msgText)
	   if len(msgBytes) > 254 {
			   msgBytes = msgBytes[:254] // SMPP max short_message length is 254
	   }
	   body = append(body, byte(len(msgBytes)))
	   body = append(body, msgBytes...)

	   return body
}

// Parse submit_sm PDU
// func (s *SMPPServer) parseSubmitSM(body []byte) (*Message, error) {
// 	offset := 0

// 	// Skip service_type
// 	_, offset = s.extractCString(body, offset)

// 	// Skip source_addr_ton, source_addr_npi
// 	offset += 2

// 	// Extract source_addr
// 	sourceAddr, offset := s.extractCString(body, offset)

// 	// Skip dest_addr_ton, dest_addr_npi
// 	offset += 2

// 	// Extract destination_addr
// 	destAddr, offset := s.extractCString(body, offset)

// 	// Extract esm_class
// 	if offset >= len(body) {
// 		return nil, fmt.Errorf("invalid PDU format")
// 	}
// 	esmClass := body[offset]
// 	offset++

// 	// Skip protocol_id, priority_flag, schedule_delivery_time, validity_period
// 	offset++
// 	offset++
// 	_, offset = s.extractCString(body, offset)
// 	_, offset = s.extractCString(body, offset)

// 	// Extract registered_delivery
// 	if offset >= len(body) {
// 		return nil, fmt.Errorf("invalid PDU format")
// 	}
// 	registeredDelivery := body[offset]
// 	offset++

// 	// Skip replace_if_present_flag
// 	offset++

// 	// Extract data_coding
// 	if offset >= len(body) {
// 		return nil, fmt.Errorf("invalid PDU format")
// 	}
// 	dataCoding := body[offset]
// 	offset++

// 	// Skip sm_default_msg_id
// 	offset++

// 	// Extract sm_length and short_message
// 	if offset >= len(body) {
// 		return nil, fmt.Errorf("invalid PDU format")
// 	}
// 	smLength := int(body[offset])
// 	offset++

// 	if offset+smLength > len(body) {
// 		return nil, fmt.Errorf("invalid message length")
// 	}

// 	shortMessage := string(body[offset : offset+smLength])

// 	return &Message{
// 		SourceAddr:         sourceAddr,
// 		DestAddr:           destAddr,
// 		ShortMessage:       shortMessage,
// 		DataCoding:         dataCoding,
// 		ESMClass:           esmClass,
// 		IsUSSD:             (esmClass & ESM_USSD) != 0,
// 		RegisteredDelivery: registeredDelivery,
// 	}, nil
// }
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
	start := offset
	for offset < len(data) && data[offset] != 0 {
		offset++
	}
	if offset < len(data) {
		offset++ // skip null terminator
	}
	return string(data[start : offset-1]), offset
}

// Send PDU to session
func (s *SMPPServer) sendPDU(session *Session, pdu *PDU) error {
	// Create buffer for PDU
	buf := new(bytes.Buffer)

	// Write header
	binary.Write(buf, binary.BigEndian, pdu.Header)

	// Write body if present
	if pdu.Body != nil {
		buf.Write(pdu.Body)
	}

	// Send to connection
	session.mutex.Lock()
	defer session.mutex.Unlock()

	_, err := session.conn.Write(buf.Bytes())
	return err
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
	// Example USSD menu handling
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
