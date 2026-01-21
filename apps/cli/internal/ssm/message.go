package ssm

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Message types used in SSM Session Manager protocol
const (
	InputStreamMessage      = "input_stream_data"
	OutputStreamMessage     = "output_stream_data"
	AcknowledgeMessage      = "acknowledge"
	ChannelClosedMessage    = "channel_closed"
	StartPublicationMessage = "start_publication"
	PausePublicationMessage = "pause_publication"
)

// Payload types (from session-manager-plugin source)
const (
	PayloadTypeOutput            uint32 = 1
	PayloadTypeError             uint32 = 2
	PayloadTypeSize              uint32 = 3
	PayloadTypeParameter         uint32 = 4
	PayloadTypeHandshakeRequest  uint32 = 5
	PayloadTypeHandshakeResponse uint32 = 6
	PayloadTypeHandshakeComplete uint32 = 7
)

// Flags
const (
	FlagData uint64 = 0
	FlagSyn  uint64 = 1
	FlagFin  uint64 = 2
	FlagAck  uint64 = 3
)

// Header size in bytes (fixed)
const HeaderLength uint32 = 116

// Message represents an SSM Session Manager protocol message
type Message struct {
	HeaderLength   uint32
	MessageType    string // 32 bytes max
	SchemaVersion  uint32
	CreatedDate    uint64
	SequenceNumber int64
	Flags          uint64
	MessageID      uuid.UUID
	PayloadDigest  []byte // 32 bytes SHA256
	PayloadType    uint32
	PayloadLength  uint32
	Payload        []byte
}

// NewMessage creates a new message with defaults
func NewMessage(messageType string, sequenceNumber int64, payload []byte) *Message {
	digest := sha256.Sum256(payload)
	return &Message{
		HeaderLength:   HeaderLength,
		MessageType:    messageType,
		SchemaVersion:  1,
		CreatedDate:    uint64(time.Now().UnixMilli()),
		SequenceNumber: sequenceNumber,
		Flags:          FlagData,
		MessageID:      uuid.New(),
		PayloadDigest:  digest[:],
		PayloadType:    PayloadTypeOutput,
		PayloadLength:  uint32(len(payload)),
		Payload:        payload,
	}
}

// NewAcknowledgeMessage creates an acknowledge message for a received message
// Note: The ack message itself uses SequenceNumber=0, the acknowledged seqNum goes in the payload
func NewAcknowledgeMessage(ackMsgType string, seqNum int64, msgID uuid.UUID) *Message {
	payload := []byte(fmt.Sprintf(
		`{"AcknowledgedMessageType":"%s","AcknowledgedMessageId":"%s","AcknowledgedMessageSequenceNumber":%d,"IsSequentialMessage":true}`,
		ackMsgType, msgID.String(), seqNum,
	))
	// Ack messages use sequence number 0
	return NewMessage(AcknowledgeMessage, 0, payload)
}

// Serialize encodes the message to binary format
func (m *Message) Serialize() ([]byte, error) {
	buf := new(bytes.Buffer)

	// HeaderLength (4 bytes)
	if err := binary.Write(buf, binary.BigEndian, m.HeaderLength); err != nil {
		return nil, err
	}

	// MessageType (32 bytes, padded with spaces to match AWS format)
	msgType := make([]byte, 32)
	for i := range msgType {
		msgType[i] = ' ' // Pad with spaces like AWS does
	}
	copy(msgType, []byte(m.MessageType))
	buf.Write(msgType)

	// SchemaVersion (4 bytes)
	if err := binary.Write(buf, binary.BigEndian, m.SchemaVersion); err != nil {
		return nil, err
	}

	// CreatedDate (8 bytes)
	if err := binary.Write(buf, binary.BigEndian, m.CreatedDate); err != nil {
		return nil, err
	}

	// SequenceNumber (8 bytes)
	if err := binary.Write(buf, binary.BigEndian, m.SequenceNumber); err != nil {
		return nil, err
	}

	// Flags (8 bytes)
	if err := binary.Write(buf, binary.BigEndian, m.Flags); err != nil {
		return nil, err
	}

	// MessageID (16 bytes UUID)
	msgIDBytes, _ := m.MessageID.MarshalBinary()
	buf.Write(msgIDBytes)

	// PayloadDigest (32 bytes)
	buf.Write(m.PayloadDigest)

	// PayloadType (4 bytes)
	if err := binary.Write(buf, binary.BigEndian, m.PayloadType); err != nil {
		return nil, err
	}

	// PayloadLength (4 bytes)
	if err := binary.Write(buf, binary.BigEndian, m.PayloadLength); err != nil {
		return nil, err
	}

	// Payload
	buf.Write(m.Payload)

	return buf.Bytes(), nil
}

// Deserialize decodes a binary message
func Deserialize(data []byte) (*Message, error) {
	if len(data) < int(HeaderLength) {
		return nil, fmt.Errorf("message too short: %d bytes", len(data))
	}

	buf := bytes.NewReader(data)
	msg := &Message{}

	// HeaderLength
	if err := binary.Read(buf, binary.BigEndian, &msg.HeaderLength); err != nil {
		return nil, err
	}

	// MessageType (32 bytes, padded with nulls or spaces)
	msgType := make([]byte, 32)
	if _, err := buf.Read(msgType); err != nil {
		return nil, err
	}
	msg.MessageType = strings.TrimRight(string(bytes.TrimRight(msgType, "\x00")), " ")

	// SchemaVersion
	if err := binary.Read(buf, binary.BigEndian, &msg.SchemaVersion); err != nil {
		return nil, err
	}

	// CreatedDate
	if err := binary.Read(buf, binary.BigEndian, &msg.CreatedDate); err != nil {
		return nil, err
	}

	// SequenceNumber
	if err := binary.Read(buf, binary.BigEndian, &msg.SequenceNumber); err != nil {
		return nil, err
	}

	// Flags
	if err := binary.Read(buf, binary.BigEndian, &msg.Flags); err != nil {
		return nil, err
	}

	// MessageID (16 bytes)
	msgIDBytes := make([]byte, 16)
	if _, err := buf.Read(msgIDBytes); err != nil {
		return nil, err
	}
	msg.MessageID, _ = uuid.FromBytes(msgIDBytes)

	// PayloadDigest (32 bytes)
	msg.PayloadDigest = make([]byte, 32)
	if _, err := buf.Read(msg.PayloadDigest); err != nil {
		return nil, err
	}

	// PayloadType
	if err := binary.Read(buf, binary.BigEndian, &msg.PayloadType); err != nil {
		return nil, err
	}

	// PayloadLength
	if err := binary.Read(buf, binary.BigEndian, &msg.PayloadLength); err != nil {
		return nil, err
	}

	// Payload
	if msg.PayloadLength > 0 {
		msg.Payload = make([]byte, msg.PayloadLength)
		if _, err := buf.Read(msg.Payload); err != nil {
			return nil, err
		}
	}

	return msg, nil
}
