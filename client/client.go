package main

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
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

// SMPP ESM Class values
const (
	ESM_DEFAULT          = 0x00
	ESM_USSD             = 0x40
	ESM_DELIVERY_RECEIPT = 0x04
)

// Registered delivery flags
const (
	RD_NONE            = 0x00
	RD_SUCCESS_FAILURE = 0x01
	RD_FAILURE         = 0x02
	RD_SUCCESS         = 0x04
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

// SMPP Client
type SMPPClient struct {
	conn       net.Conn
	systemID   string
	password   string
	bound      bool
	sequenceNo uint32
	mutex      sync.Mutex
	serverAddr string
	serverPort int
}

// Message for sending
type ClientMessage struct {
	From           string
	To             string
	Text           string
	IsUSSD         bool
	RequestReceipt bool
}

// --- Configuration Struct for Client ---
// ClientConfig holds the client configuration parameters
type ClientConfig struct {
	ServerAddr string `json:"ServerAddr"`
	ServerPort int    `json:"ServerPort"`
	SystemID   string `json:"SystemID"`
	Password   string `json:"Password"`
}

// NewSMPPClient creates a new SMPP client
func NewSMPPClient(serverAddr string, serverPort int, systemID, password string) *SMPPClient {
	return &SMPPClient{
		serverAddr: serverAddr,
		serverPort: serverPort,
		systemID:   systemID,
		password:   password,
	}
}

// --- New function to load client configuration ---
// LoadClientConfig reads the client configuration from a JSON file
func LoadClientConfig(filePath string) (*ClientConfig, error) {
	file, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read client config file %s: %w", filePath, err)
	}

	var config ClientConfig
	err = json.Unmarshal(file, &config)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal client config JSON from %s: %w", filePath, err)
	}

	return &config, nil
}

// Connect to SMPP server
func (c *SMPPClient) Connect() error {
	var err error
	c.conn, err = net.Dial("tcp", fmt.Sprintf("%s:%d", c.serverAddr, c.serverPort))
	if err != nil {
		return fmt.Errorf("failed to connect: %v", err)
	}

	log.Printf("Connected to SMPP server at %s:%d", c.serverAddr, c.serverPort)

	// Start receiving PDUs
	go c.receivePDUs()

	return nil
}

// Bind as transceiver
func (c *SMPPClient) Bind() error {
	c.mutex.Lock()
	c.sequenceNo++
	seqNo := c.sequenceNo
	c.mutex.Unlock()

	// Build bind_transceiver PDU
	body := []byte(c.systemID)
	body = append(body, 0) // null terminator
	body = append(body, []byte(c.password)...)
	body = append(body, 0)                       // null terminator
	body = append(body, []byte("SMPPClient")...) // system_type
	body = append(body, 0)                       // null terminator
	body = append(body, 0x34)                    // interface_version
	body = append(body, 0)                       // addr_ton
	body = append(body, 0)                       // addr_npi
	body = append(body, 0)                       // address_range (null)

	pdu := &PDU{
		Header: PDUHeader{
			CommandLength: uint32(16 + len(body)),
			CommandID:     BIND_TRANSCEIVER,
			CommandStatus: 0,
			SequenceNo:    seqNo,
		},
		Body: body,
	}

	err := c.sendPDU(pdu)
	if err != nil {
		return fmt.Errorf("failed to send bind: %v", err)
	}

	log.Printf("Sent bind_transceiver request")
	return nil
}

// Send SMS or USSD
func (c *SMPPClient) SendMessage(msg *ClientMessage) (string, error) {
	if !c.bound {
		return "", fmt.Errorf("client not bound")
	}

	c.mutex.Lock()
	c.sequenceNo++
	seqNo := c.sequenceNo
	c.mutex.Unlock()

	// Build submit_sm PDU
	body := c.buildSubmitSMBody(msg)

	pdu := &PDU{
		Header: PDUHeader{
			CommandLength: uint32(16 + len(body)),
			CommandID:     SUBMIT_SM,
			CommandStatus: 0,
			SequenceNo:    seqNo,
		},
		Body: body,
	}

	err := c.sendPDU(pdu)
	if err != nil {
		return "", fmt.Errorf("failed to send message: %v", err)
	}

	msgType := "SMS"
	if msg.IsUSSD {
		msgType = "USSD"
	}

	log.Printf("Sent %s from %s to %s: %s", msgType, msg.From, msg.To, msg.Text)
	return fmt.Sprintf("SEQ%d", seqNo), nil
}

// Send enquire_link
func (c *SMPPClient) SendEnquireLink() error {
	c.mutex.Lock()
	c.sequenceNo++
	seqNo := c.sequenceNo
	c.mutex.Unlock()

	pdu := &PDU{
		Header: PDUHeader{
			CommandLength: 16,
			CommandID:     ENQUIRE_LINK,
			CommandStatus: 0,
			SequenceNo:    seqNo,
		},
	}

	return c.sendPDU(pdu)
}

// Unbind from server
func (c *SMPPClient) Unbind() error {
	c.mutex.Lock()
	c.sequenceNo++
	seqNo := c.sequenceNo
	c.mutex.Unlock()

	pdu := &PDU{
		Header: PDUHeader{
			CommandLength: 16,
			CommandID:     UNBIND,
			CommandStatus: 0,
			SequenceNo:    seqNo,
		},
	}

	err := c.sendPDU(pdu)
	if err != nil {
		return err
	}

	c.bound = false
	return nil
}

// Build submit_sm body
func (c *SMPPClient) buildSubmitSMBody(msg *ClientMessage) []byte {
	var body []byte

	// service_type (null)
	body = append(body, 0)

	// source_addr_ton, source_addr_npi
	body = append(body, 0, 0)

	// source_addr
	body = append(body, []byte(msg.From)...)
	body = append(body, 0)

	// dest_addr_ton, dest_addr_npi
	body = append(body, 0, 0)

	// destination_addr
	body = append(body, []byte(msg.To)...)
	body = append(body, 0)

	// esm_class
	esmClass := byte(ESM_DEFAULT)
	if msg.IsUSSD {
		esmClass = ESM_USSD
	}
	body = append(body, esmClass)

	// protocol_id
	body = append(body, 0)

	// priority_flag
	body = append(body, 0)

	// schedule_delivery_time (null)
	body = append(body, 0)

	// validity_period (null)
	body = append(body, 0)

	// registered_delivery
	registeredDelivery := byte(RD_NONE)
	if msg.RequestReceipt && !msg.IsUSSD {
		registeredDelivery = RD_SUCCESS_FAILURE
	}
	body = append(body, registeredDelivery)

	// replace_if_present_flag
	body = append(body, 0)

	// data_coding
	body = append(body, 0)

	// sm_default_msg_id
	body = append(body, 0)

	// sm_length
	msgBytes := []byte(msg.Text)
	body = append(body, byte(len(msgBytes)))

	// short_message
	body = append(body, msgBytes...)

	return body
}

// Send PDU
func (c *SMPPClient) sendPDU(pdu *PDU) error {
	buf := new(bytes.Buffer)

	// Write header
	binary.Write(buf, binary.BigEndian, pdu.Header)

	// Write body if present
	if pdu.Body != nil {
		buf.Write(pdu.Body)
	}

	_, err := c.conn.Write(buf.Bytes())
	return err
}

// Receive PDUs
func (c *SMPPClient) receivePDUs() {
	for {
		pdu, err := c.readPDU()
		if err != nil {
			log.Printf("Error reading PDU: %v", err)
			break
		}

		c.handlePDU(pdu)
	}
}

// Read PDU
func (c *SMPPClient) readPDU() (*PDU, error) {
	// Read header first (16 bytes)
	headerBuf := make([]byte, 16)
	_, err := c.conn.Read(headerBuf)
	if err != nil {
		return nil, err
	}

	var header PDUHeader
	buf := bytes.NewReader(headerBuf)
	binary.Read(buf, binary.BigEndian, &header)

	// Read body if present
	bodyLen := header.CommandLength - 16
	var body []byte
	if bodyLen > 0 {
		body = make([]byte, bodyLen)
		_, err = c.conn.Read(body)
		if err != nil {
			return nil, err
		}
	}

	return &PDU{
		Header: header,
		Body:   body,
	}, nil
}

// Handle received PDU
func (c *SMPPClient) handlePDU(pdu *PDU) {
	switch pdu.Header.CommandID {
	case BIND_TRANSCEIVER_RESP:
		c.handleBindResp(pdu)
	case SUBMIT_SM_RESP:
		c.handleSubmitSMResp(pdu)
	case DELIVER_SM:
		c.handleDeliverSM(pdu)
	case ENQUIRE_LINK_RESP:
		log.Printf("Received enquire_link_resp")
	case UNBIND_RESP:
		log.Printf("Received unbind_resp")
	default:
		log.Printf("Received unknown PDU: 0x%08X", pdu.Header.CommandID)
	}
}

// Handle bind response
func (c *SMPPClient) handleBindResp(pdu *PDU) {
	if pdu.Header.CommandStatus == ESME_ROK {
		c.bound = true
		log.Printf("Successfully bound to server")
	} else {
		log.Printf("Bind failed with status: 0x%08X", pdu.Header.CommandStatus)
	}
}

// Handle submit_sm response
func (c *SMPPClient) handleSubmitSMResp(pdu *PDU) {
	if pdu.Header.CommandStatus == ESME_ROK {
		messageID, _ := c.extractCString(pdu.Body, 0)
		log.Printf("Message accepted with ID: %s", messageID)
	} else {
		log.Printf("Message rejected with status: 0x%08X", pdu.Header.CommandStatus)
	}
}

// Handle deliver_sm
func (c *SMPPClient) handleDeliverSM(pdu *PDU) {
	msg := c.parseDeliverSM(pdu.Body)

	if msg.ESMClass&ESM_DELIVERY_RECEIPT != 0 {
		log.Printf("📧 DELIVERY RECEIPT: %s", msg.ShortMessage)
	} else if msg.ESMClass&ESM_USSD != 0 {
		log.Printf("📱 USSD Response from %s: %s", msg.SourceAddr, msg.ShortMessage)
	} else {
		log.Printf("📨 SMS from %s to %s: %s", msg.SourceAddr, msg.DestAddr, msg.ShortMessage)
	}

	// Send deliver_sm_resp
	c.sendDeliverSMResp(pdu.Header.SequenceNo)
}

// Parse deliver_sm
func (c *SMPPClient) parseDeliverSM(body []byte) *DeliverSMMessage {
	offset := 0

	// Skip service_type
	_, offset = c.extractCString(body, offset)

	// Skip source_addr_ton, source_addr_npi
	offset += 2

	// Extract source_addr
	sourceAddr, offset := c.extractCString(body, offset)

	// Skip dest_addr_ton, dest_addr_npi
	offset += 2

	// Extract destination_addr
	destAddr, offset := c.extractCString(body, offset)

	// Extract esm_class
	esmClass := body[offset]
	offset++

	// Skip protocol_id, priority_flag, schedule_delivery_time, validity_period
	offset += 2
	_, offset = c.extractCString(body, offset)
	_, offset = c.extractCString(body, offset)

	// Skip registered_delivery, replace_if_present_flag, data_coding, sm_default_msg_id
	offset += 4

	// Extract sm_length and short_message
	smLength := int(body[offset])
	offset++

	shortMessage := string(body[offset : offset+smLength])

	return &DeliverSMMessage{
		SourceAddr:   sourceAddr,
		DestAddr:     destAddr,
		ShortMessage: shortMessage,
		ESMClass:     esmClass,
	}
}

// Send deliver_sm response
func (c *SMPPClient) sendDeliverSMResp(seqNo uint32) {
	pdu := &PDU{
		Header: PDUHeader{
			CommandLength: 17, // 16 + 1 for message_id (null)
			CommandID:     DELIVER_SM_RESP,
			CommandStatus: ESME_ROK,
			SequenceNo:    seqNo,
		},
		Body: []byte{0}, // message_id (null)
	}

	c.sendPDU(pdu)
}

// Extract C string from byte array
func (c *SMPPClient) extractCString(data []byte, offset int) (string, int) {
	start := offset
	for offset < len(data) && data[offset] != 0 {
		offset++
	}
	if offset < len(data) {
		offset++ // skip null terminator
	}
	return string(data[start : offset-1]), offset
}

// DeliverSM message structure
type DeliverSMMessage struct {
	SourceAddr   string
	DestAddr     string
	ShortMessage string
	ESMClass     byte
}

// Close connection
func (c *SMPPClient) Close() error {
	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}

// Interactive menu
func (c *SMPPClient) runInteractiveMenu() {
	scanner := bufio.NewScanner(os.Stdin)

	for {
		fmt.Println("\n=== SMPP Client Menu ===")
		fmt.Println("1. Send SMS")
		fmt.Println("2. Send SMS with Delivery Receipt")
		fmt.Println("3. Send USSD Request")
		fmt.Println("4. Send Enquire Link")
		fmt.Println("5. Send Bulk SMS (10 messages)")
		fmt.Println("6. Exit")
		fmt.Print("Choose option: ")

		if !scanner.Scan() {
			break
		}

		choice := strings.TrimSpace(scanner.Text())

		switch choice {
		case "1":
			c.sendSMSMenu(scanner, false)
		case "2":
			c.sendSMSMenu(scanner, true)
		case "3":
			c.sendUSSDMenu(scanner)
		case "4":
			c.SendEnquireLink()
		case "5":
			c.sendBulkSMS()
		case "6":
			fmt.Println("Exiting...")
			return
		default:
			fmt.Println("Invalid option")
		}
	}
}

// Send SMS menu
func (c *SMPPClient) sendSMSMenu(scanner *bufio.Scanner, requestReceipt bool) {
	fmt.Print("From number: ")
	if !scanner.Scan() {
		return
	}
	from := strings.TrimSpace(scanner.Text())

	fmt.Print("To number: ")
	if !scanner.Scan() {
		return
	}
	to := strings.TrimSpace(scanner.Text())

	fmt.Print("Message text: ")
	if !scanner.Scan() {
		return
	}
	text := strings.TrimSpace(scanner.Text())

	msg := &ClientMessage{
		From:           from,
		To:             to,
		Text:           text,
		IsUSSD:         false,
		RequestReceipt: requestReceipt,
	}

	_, err := c.SendMessage(msg)
	if err != nil {
		fmt.Printf("Error sending SMS: %v\n", err)
	}
}

// Send USSD menu
func (c *SMPPClient) sendUSSDMenu(scanner *bufio.Scanner) {
	fmt.Print("From number: ")
	if !scanner.Scan() {
		return
	}
	from := strings.TrimSpace(scanner.Text())

	fmt.Print("To number: ")
	if !scanner.Scan() {
		return
	}
	to := strings.TrimSpace(scanner.Text())

	fmt.Print("USSD code (e.g., *123#): ")
	if !scanner.Scan() {
		return
	}
	code := strings.TrimSpace(scanner.Text())

	msg := &ClientMessage{
		From:   from,
		To:     to,
		Text:   code,
		IsUSSD: true,
	}

	_, err := c.SendMessage(msg)
	if err != nil {
		fmt.Printf("Error sending USSD: %v\n", err)
	}
}

// Send bulk SMS for testing
func (c *SMPPClient) sendBulkSMS() {
	fmt.Println("Sending 10 test SMS messages...")

	for i := 1; i <= 10; i++ {
		msg := &ClientMessage{
			From:           "1234",
			To:             fmt.Sprintf("555%04d", i),
			Text:           fmt.Sprintf("Test message #%d", i),
			IsUSSD:         false,
			RequestReceipt: i%2 == 0, // Request receipt for even numbers
		}

		_, err := c.SendMessage(msg)
		if err != nil {
			fmt.Printf("Error sending message %d: %v\n", i, err)
		}

		time.Sleep(100 * time.Millisecond) // Small delay between messages
	}

	fmt.Println("Bulk SMS sending completed")
}

// Main function
func main() {
	// --- Load configuration from file ---
	config, err := LoadClientConfig("client_config.json") // Adjusted path
	if err != nil {
		log.Fatalf("Failed to load client configuration: %v", err)
	}

	fmt.Printf("🚀 Starting SMPP Client\n")
	fmt.Printf("Server: %s:%d\n", config.ServerAddr, config.ServerPort)
	fmt.Printf("System ID: %s\n", config.SystemID)

	// Create client using loaded configuration
	client := NewSMPPClient(config.ServerAddr, config.ServerPort, config.SystemID, config.Password)
	defer client.Close()
	// Connect
	err = client.Connect()
	if err != nil {
		log.Fatal(err)
	}

	// Bind
	err = client.Bind()
	if err != nil {
		log.Fatal(err)
	}

	// Wait for bind response
	time.Sleep(1 * time.Second)

	if !client.bound {
		log.Fatal("Failed to bind to server")
	}

	// Start enquire_link timer
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()

		for range ticker.C {
			client.SendEnquireLink()
		}
	}()

	// Run interactive menu
	client.runInteractiveMenu()

	// Unbind before exit
	client.Unbind()
	time.Sleep(1 * time.Second)
}
