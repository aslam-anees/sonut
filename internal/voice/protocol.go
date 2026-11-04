package voice

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

const (
	ProtocolVersion = 1
	MaxProtocolLine = 1024 * 1024
)

// Message is the versioned NDJSON envelope shared by Go and atcode-voice.
// Unknown fields are ignored for forward compatibility, while version and
// type are mandatory to avoid accepting arbitrary helper stdout.
type Message struct {
	Version      int            `json:"version"`
	Type         string         `json:"type"`
	RequestID    string         `json:"requestId,omitempty"`
	UtteranceID  string         `json:"utteranceId,omitempty"`
	Text         string         `json:"text,omitempty"`
	Language     string         `json:"language,omitempty"`
	Voice        string         `json:"voice,omitempty"`
	Speed        float64        `json:"speed,omitempty"`
	SilenceMs    int            `json:"silenceMs,omitempty"`
	Profile      string         `json:"profile,omitempty"`
	InputDevice  string         `json:"inputDevice,omitempty"`
	OutputDevice string         `json:"outputDevice,omitempty"`
	State        string         `json:"state,omitempty"`
	Error        string         `json:"error,omitempty"`
	Devices      []Device       `json:"devices,omitempty"`
	Metrics      map[string]any `json:"metrics,omitempty"`
}

type Device struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Direction string `json:"direction"`
}

func (m Message) Validate() error {
	if m.Version != ProtocolVersion {
		return fmt.Errorf("unsupported voice protocol version %d", m.Version)
	}
	if strings.TrimSpace(m.Type) == "" {
		return fmt.Errorf("voice protocol message has no type")
	}
	return nil
}

func EncodeMessage(w io.Writer, message Message) error {
	if message.Version == 0 {
		message.Version = ProtocolVersion
	}
	if err := message.Validate(); err != nil {
		return err
	}
	data, err := json.Marshal(message)
	if err != nil {
		return err
	}
	if len(data) > MaxProtocolLine {
		return fmt.Errorf("voice protocol message exceeds %d bytes", MaxProtocolLine)
	}
	_, err = fmt.Fprintf(w, "%s\n", data)
	return err
}

func DecodeMessage(line []byte) (Message, error) {
	if len(line) > MaxProtocolLine {
		return Message{}, fmt.Errorf("voice protocol line exceeds %d bytes", MaxProtocolLine)
	}
	var message Message
	if err := json.Unmarshal(line, &message); err != nil {
		return Message{}, err
	}
	if err := message.Validate(); err != nil {
		return Message{}, err
	}
	return message, nil
}

func ReadMessages(r io.Reader, handle func(Message) error) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 4096), MaxProtocolLine)
	for scanner.Scan() {
		message, err := DecodeMessage(scanner.Bytes())
		if err != nil {
			return err
		}
		if err := handle(message); err != nil {
			return err
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return io.EOF
}
