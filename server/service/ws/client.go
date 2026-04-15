package ws

import (
	"encoding/json"
	"time"

	"github.com/gorilla/websocket"
	log "github.com/sirupsen/logrus"
)

const (
	Heartbeat = iota
	KeyboardEvent
	MouseEvent
)

func NewClient(ws *websocket.Conn) *Client {
	return &Client{
		ws:            ws,
		lastHeartbeat: time.Time{},
	}
}

func (c *Client) Start() {
	defer c.Close()
	_ = c.Read()
}

func (c *Client) Read() error {
	var zeroTime time.Time
	_ = c.ws.SetReadDeadline(zeroTime)

	for {
		messageType, data, err := c.ws.ReadMessage()
		if err != nil {
			return err
		}

		if len(data) == 0 {
			continue
		}

		log.Debugf("received message %d: %v", messageType, data)

		switch data[0] {
		case Heartbeat:
			c.UpdateHeartbeat()
		case KeyboardEvent, MouseEvent:
			// HID removed; input events are dropped
		}
	}
}

func (c *Client) Write(event string, data string) error {
	message := &Message{
		Type: event,
		Data: data,
	}

	messageByte, err := json.Marshal(message)
	if err != nil {
		log.Errorf("failed to marshal message: %s", err)
		return err
	}

	c.mutex.Lock()
	defer c.mutex.Unlock()

	_ = c.ws.SetWriteDeadline(time.Now().Add(10 * time.Second))
	return c.ws.WriteMessage(websocket.TextMessage, messageByte)
}

func (c *Client) UpdateHeartbeat() {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	c.lastHeartbeat = time.Now()
}

func (c *Client) Close() {
	_ = c.ws.Close()
	log.Debug("websocket disconnected")
}
