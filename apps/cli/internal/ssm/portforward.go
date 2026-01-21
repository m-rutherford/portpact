package ssm

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"net"
	"sync"
	"sync/atomic"
)

// Port forwarding protocol message types (inside the SSM payload)
const (
	FlagTypeData = 0
	FlagTypeSyn  = 1
	FlagTypeFin  = 2
	FlagTypeAck  = 3
)

// PortForwardPayload represents the port forwarding data format
type PortForwardPayload struct {
	Type          uint32
	StreamID      uint32
	PortNumber    uint32 // Only for Syn
	OriginAddress string // Only for Syn
	Data          []byte
}

// PortForwarder handles local port forwarding through an SSM session
type PortForwarder struct {
	session       *Session
	localAddr     string
	listener      net.Listener
	streamCounter uint32
	streams       sync.Map // streamID -> *stream
	wg            sync.WaitGroup
	closeCh       chan struct{}
	closeOnce     sync.Once
}

type stream struct {
	id       uint32
	conn     net.Conn
	closeCh  chan struct{}
	closeOnce sync.Once
}

// NewPortForwarder creates a port forwarder for the given session
func NewPortForwarder(session *Session, localPort int) *PortForwarder {
	return &PortForwarder{
		session:   session,
		localAddr: fmt.Sprintf("127.0.0.1:%d", localPort),
		closeCh:   make(chan struct{}),
	}
}

// Start begins listening on the local port and forwarding connections
func (pf *PortForwarder) Start(ctx context.Context) error {
	listener, err := net.Listen("tcp", pf.localAddr)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %w", pf.localAddr, err)
	}
	pf.listener = listener

	log.Printf("✅ Listening on %s", pf.localAddr)

	// Handle incoming messages from the SSM session
	pf.wg.Add(1)
	go pf.handleIncoming()

	// Accept connections
	pf.wg.Add(1)
	go pf.acceptConnections(ctx)

	return nil
}

// acceptConnections accepts new TCP connections and creates streams
func (pf *PortForwarder) acceptConnections(ctx context.Context) {
	defer pf.wg.Done()

	for {
		select {
		case <-pf.closeCh:
			return
		case <-ctx.Done():
			return
		default:
		}

		conn, err := pf.listener.Accept()
		if err != nil {
			select {
			case <-pf.closeCh:
				return
			default:
				log.Printf("accept error: %v", err)
				continue
			}
		}

		streamID := atomic.AddUint32(&pf.streamCounter, 1)
		s := &stream{
			id:      streamID,
			conn:    conn,
			closeCh: make(chan struct{}),
		}
		pf.streams.Store(streamID, s)

		// Send SYN to initiate the port forward for this stream
		if err := pf.sendSyn(streamID); err != nil {
			log.Printf("failed to send SYN for stream %d: %v", streamID, err)
			conn.Close()
			pf.streams.Delete(streamID)
			continue
		}

		// Start reading from local connection
		pf.wg.Add(1)
		go pf.handleLocalConn(s)
	}
}

// handleLocalConn reads from a local connection and sends data through SSM
func (pf *PortForwarder) handleLocalConn(s *stream) {
	defer pf.wg.Done()
	defer pf.closeStream(s.id)

	buf := make([]byte, 4096)
	for {
		select {
		case <-s.closeCh:
			return
		case <-pf.closeCh:
			return
		default:
		}

		n, err := s.conn.Read(buf)
		if err != nil {
			if err != io.EOF {
				log.Printf("stream %d read error: %v", s.id, err)
			}
			// Send FIN
			pf.sendFin(s.id)
			return
		}

		if n > 0 {
			if err := pf.sendData(s.id, buf[:n]); err != nil {
				log.Printf("stream %d send error: %v", s.id, err)
				return
			}
		}
	}
}

// handleIncoming processes messages from the SSM session
func (pf *PortForwarder) handleIncoming() {
	defer pf.wg.Done()

	for {
		select {
		case <-pf.closeCh:
			return
		case msg := <-pf.session.RecvChan():
			if msg == nil {
				return
			}
			pf.processMessage(msg)
		}
	}
}

// processMessage handles an incoming SSM message
func (pf *PortForwarder) processMessage(msg *Message) {
	if msg.MessageType != OutputStreamMessage {
		return
	}

	if len(msg.Payload) == 0 {
		return
	}

	// Parse port forward payload
	payload, err := parsePortForwardPayload(msg.Payload)
	if err != nil {
		// May be a handshake or other message type
		return
	}

	switch payload.Type {
	case FlagTypeData:
		pf.handleData(payload)
	case FlagTypeAck:
		// ACK received, stream is established
	case FlagTypeFin:
		pf.closeStream(payload.StreamID)
	}
}

// handleData writes received data to the local connection
func (pf *PortForwarder) handleData(payload *PortForwardPayload) {
	val, ok := pf.streams.Load(payload.StreamID)
	if !ok {
		return
	}
	s := val.(*stream)

	if len(payload.Data) > 0 {
		_, err := s.conn.Write(payload.Data)
		if err != nil {
			log.Printf("stream %d write error: %v", s.id, err)
			pf.closeStream(s.id)
		}
	}
}

// closeStream closes a stream
func (pf *PortForwarder) closeStream(streamID uint32) {
	val, ok := pf.streams.LoadAndDelete(streamID)
	if !ok {
		return
	}
	s := val.(*stream)
	s.closeOnce.Do(func() {
		close(s.closeCh)
		s.conn.Close()
	})
}

// sendSyn sends a SYN message to establish the stream
func (pf *PortForwarder) sendSyn(streamID uint32) error {
	payload := serializePortForwardPayload(&PortForwardPayload{
		Type:     FlagTypeSyn,
		StreamID: streamID,
	})
	return pf.session.SendData(payload)
}

// sendData sends data for a stream
func (pf *PortForwarder) sendData(streamID uint32, data []byte) error {
	payload := serializePortForwardPayload(&PortForwardPayload{
		Type:     FlagTypeData,
		StreamID: streamID,
		Data:     data,
	})
	return pf.session.SendData(payload)
}

// sendFin sends a FIN to close a stream
func (pf *PortForwarder) sendFin(streamID uint32) error {
	payload := serializePortForwardPayload(&PortForwardPayload{
		Type:     FlagTypeFin,
		StreamID: streamID,
	})
	return pf.session.SendData(payload)
}

// Close shuts down the port forwarder
func (pf *PortForwarder) Close() {
	pf.closeOnce.Do(func() {
		close(pf.closeCh)
		if pf.listener != nil {
			pf.listener.Close()
		}
		// Close all streams
		pf.streams.Range(func(key, _ interface{}) bool {
			pf.closeStream(key.(uint32))
			return true
		})
	})
}

// Wait waits for all goroutines to finish
func (pf *PortForwarder) Wait() {
	pf.wg.Wait()
}

// parsePortForwardPayload parses port forward payload data
func parsePortForwardPayload(data []byte) (*PortForwardPayload, error) {
	if len(data) < 8 { // Minimum: type(4) + streamID(4)
		return nil, fmt.Errorf("payload too short")
	}

	buf := bytes.NewReader(data)
	payload := &PortForwardPayload{}

	if err := binary.Read(buf, binary.BigEndian, &payload.Type); err != nil {
		return nil, err
	}
	if err := binary.Read(buf, binary.BigEndian, &payload.StreamID); err != nil {
		return nil, err
	}

	// Rest is data
	if buf.Len() > 0 {
		payload.Data = make([]byte, buf.Len())
		buf.Read(payload.Data)
	}

	return payload, nil
}

// serializePortForwardPayload creates the binary format for port forwarding
func serializePortForwardPayload(p *PortForwardPayload) []byte {
	buf := new(bytes.Buffer)
	binary.Write(buf, binary.BigEndian, p.Type)
	binary.Write(buf, binary.BigEndian, p.StreamID)
	if len(p.Data) > 0 {
		buf.Write(p.Data)
	}
	return buf.Bytes()
}
