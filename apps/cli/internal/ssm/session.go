package ssm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

// SessionCredentials holds the credentials returned by the broker
type SessionCredentials struct {
	SessionID  string `json:"sessionId"`
	StreamURL  string `json:"streamUrl"`
	TokenValue string `json:"tokenValue"`
	Target     struct {
		Host string `json:"host"`
		Port int    `json:"port"`
	} `json:"target"`
	LocalPort int `json:"localPort"`
}

// Handshake types
type HandshakeRequest struct {
	AgentVersion           string                 `json:"AgentVersion"`
	RequestedClientActions []RequestedClientAction `json:"RequestedClientActions"`
}

type RequestedClientAction struct {
	ActionType       string          `json:"ActionType"`
	ActionParameters json.RawMessage `json:"ActionParameters"`
}

type HandshakeResponse struct {
	ClientVersion          string                   `json:"ClientVersion"`
	ProcessedClientActions []ProcessedClientAction  `json:"ProcessedClientActions"`
	Errors                 []string                 `json:"Errors,omitempty"`
}

type ProcessedClientAction struct {
	ActionType   string `json:"ActionType"`
	ActionStatus int    `json:"ActionStatus"` // 1 = Success, 2 = Failed
	ActionResult json.RawMessage `json:"ActionResult,omitempty"`
	Error        string `json:"Error,omitempty"`
}

// Session manages an SSM WebSocket session
type Session struct {
	creds            *SessionCredentials
	conn             *websocket.Conn
	sendCh           chan []byte
	recvCh           chan *Message
	closeCh          chan struct{}
	closeOnce        sync.Once
	seqNum           int64
	inSeqNum         int64
	mu               sync.Mutex
	isConnected      atomic.Bool
	handshakeDone    chan struct{}
	handshakeSent    atomic.Bool // Only send handshake response once
}

// NewSession creates a new session with the given credentials
func NewSession(creds *SessionCredentials) *Session {
	return &Session{
		creds:         creds,
		sendCh:        make(chan []byte, 100),
		recvCh:        make(chan *Message, 100),
		closeCh:       make(chan struct{}),
		handshakeDone: make(chan struct{}),
	}
}

// Connect establishes the WebSocket connection and performs handshake
func (s *Session) Connect(ctx context.Context) error {
	dialer := websocket.Dialer{
		HandshakeTimeout: 30 * time.Second,
	}

	conn, _, err := dialer.DialContext(ctx, s.creds.StreamURL, nil)
	if err != nil {
		return fmt.Errorf("websocket dial failed: %w", err)
	}
	s.conn = conn

	// Send the token to authenticate
	if err := s.sendToken(); err != nil {
		conn.Close()
		return fmt.Errorf("token handshake failed: %w", err)
	}

	s.isConnected.Store(true)

	// Start message pump goroutines
	go s.readPump()
	go s.writePump()

	// Wait for handshake to complete
	select {
	case <-s.handshakeDone:
		log.Printf("Handshake completed")
	case <-time.After(10 * time.Second):
		s.Close()
		return fmt.Errorf("handshake timeout")
	case <-ctx.Done():
		s.Close()
		return ctx.Err()
	}

	return nil
}

// sendToken sends the authentication token as the first message
func (s *Session) sendToken() error {
	// OpenDataChannelInput message format from AWS session-manager-plugin
	openDataChannelInput := map[string]string{
		"MessageSchemaVersion": "1.0",
		"RequestId":            s.creds.SessionID,
		"TokenValue":           s.creds.TokenValue,
		"ClientId":             uuid.New().String(),
	}
	tokenBytes, err := json.Marshal(openDataChannelInput)
	if err != nil {
		return err
	}
	

	return s.conn.WriteMessage(websocket.TextMessage, tokenBytes)
}

// readPump reads messages from the WebSocket
func (s *Session) readPump() {
	defer s.Close()

	for {
		select {
		case <-s.closeCh:
			return
		default:
		}

		_, data, err := s.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Printf("websocket read error: %v", err)
			}
			return
		}


		// Try to parse as SSM message
		msg, err := Deserialize(data)
		if err != nil {
			log.Printf("message deserialize error (len=%d): %v", len(data), err)
			// Try to print as string in case it's JSON
			if len(data) < 1000 {
				log.Printf("DEBUG: raw as string: %s", string(data))
			}
			continue
		}


		// Handle message based on type
		s.handleMessage(msg)
	}
}

// handleMessage processes incoming messages
func (s *Session) handleMessage(msg *Message) {
	atomic.StoreInt64(&s.inSeqNum, msg.SequenceNumber)

	switch msg.MessageType {
	case OutputStreamMessage:
		// Send acknowledgment
		s.sendAck(msg)
		
		// Check for handshake messages by looking at payload content
		if msg.PayloadLength > 0 && len(msg.Payload) > 0 {
			// Detect handshake complete (PayloadType 7)
			if msg.PayloadType == PayloadTypeHandshakeComplete ||
			   bytes.Contains(msg.Payload, []byte("HandshakeTimeToComplete")) {
				log.Printf("Handshake completed")
				select {
				case <-s.handshakeDone:
				default:
					close(s.handshakeDone)
				}
				return
			}
			
			// Only process handshake requests before handshake is complete
			if bytes.Contains(msg.Payload, []byte("AgentVersion")) && 
			   bytes.Contains(msg.Payload, []byte("RequestedClientActions")) {
				// Check if handshake is already done
				select {
				case <-s.handshakeDone:
					// Handshake already done, ignore retry
					return
				default:
					s.handleHandshakeRequest(msg)
					return
				}
			}
		}

		// Forward data messages to receive channel
		select {
		case s.recvCh <- msg:
		case <-s.closeCh:
		}

	case AcknowledgeMessage:
		// Acknowledgment received, nothing to do

	case ChannelClosedMessage:
		log.Printf("Channel closed by server")
		s.Close()
	}
}

// handleHandshakeRequest responds to the handshake from AWS
func (s *Session) handleHandshakeRequest(msg *Message) {
	// Only respond once
	if s.handshakeSent.Swap(true) {
		return // Already sent response
	}

	var req HandshakeRequest
	if err := json.Unmarshal(msg.Payload, &req); err != nil {
		log.Printf("Failed to parse handshake request: %v", err)
		s.handshakeSent.Store(false)
		return
	}


	// Build response - must include processed actions for each requested action
	response := HandshakeResponse{
		ClientVersion:          "1.2.0.0",
		ProcessedClientActions: make([]ProcessedClientAction, 0, len(req.RequestedClientActions)),
		// Errors is omitempty, so nil means no "Errors" field in JSON
	}

	for _, action := range req.RequestedClientActions {
		processed := ProcessedClientAction{
			ActionType:   action.ActionType,
			ActionStatus: 1, // 1 = Success
		}

		// For SessionType action, echo back the parameters
		if action.ActionType == "SessionType" {
			processed.ActionResult = action.ActionParameters
		}

		response.ProcessedClientActions = append(response.ProcessedClientActions, processed)
	}

	respBytes, err := json.Marshal(response)
	if err != nil {
		log.Printf("Failed to marshal handshake response: %v", err)
		s.handshakeSent.Store(false)
		return
	}

	// Send handshake response with PayloadType 6 (HandshakeResponse) and FlagSyn
	// Use SequenceNumber=0 to match the handshake request
	respMsg := NewMessage(InputStreamMessage, 0, respBytes)
	respMsg.PayloadType = PayloadTypeHandshakeResponse // PayloadType 6
	respMsg.Flags = FlagSyn                            // Must match the Flags from incoming handshake request

	data, err := respMsg.Serialize()
	if err != nil {
		log.Printf("Failed to serialize handshake response: %v", err)
		s.handshakeSent.Store(false)
		return
	}

	select {
	case s.sendCh <- data:
	case <-s.closeCh:
	}
}

// sendAck sends an acknowledgment for a received message
func (s *Session) sendAck(msg *Message) {
	ack := NewAcknowledgeMessage(msg.MessageType, msg.SequenceNumber, msg.MessageID)
	data, err := ack.Serialize()
	if err != nil {
		return
	}
	select {
	case s.sendCh <- data:
	case <-s.closeCh:
	}
}

// writePump sends messages to the WebSocket
func (s *Session) writePump() {
	ticker := time.NewTicker(5 * time.Second) // More frequent keepalive
	defer ticker.Stop()
	defer s.Close()

	for {
		select {
		case <-s.closeCh:
			return
		case data := <-s.sendCh:
			if err := s.conn.WriteMessage(websocket.BinaryMessage, data); err != nil {
				log.Printf("websocket write error: %v", err)
				return
			}
		case <-ticker.C:
			// Send keepalive ping
			if err := s.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				log.Printf("ping error: %v", err)
				return
			}
		}
	}
}

// SendData sends data through the session
func (s *Session) SendData(data []byte) error {
	seqNum := atomic.AddInt64(&s.seqNum, 1)
	msg := NewMessage(InputStreamMessage, seqNum, data)
	msgData, err := msg.Serialize()
	if err != nil {
		return err
	}

	select {
	case s.sendCh <- msgData:
		return nil
	case <-s.closeCh:
		return fmt.Errorf("session closed")
	}
}

// RecvChan returns the channel for receiving messages
func (s *Session) RecvChan() <-chan *Message {
	return s.recvCh
}

// Close closes the session
func (s *Session) Close() {
	s.closeOnce.Do(func() {
		close(s.closeCh)
		s.isConnected.Store(false)
		if s.conn != nil {
			s.conn.Close()
		}
	})
}

// IsConnected returns whether the session is connected
func (s *Session) IsConnected() bool {
	return s.isConnected.Load()
}

// SessionID returns the session ID
func (s *Session) SessionID() string {
	return s.creds.SessionID
}
