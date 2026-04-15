package ws

import (
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

type Manager struct {
	clients map[*websocket.Conn]*Client
	mutex   sync.RWMutex
}

type Client struct {
	ws            *websocket.Conn
	lastHeartbeat time.Time
	mutex         sync.Mutex
}

type Message struct {
	Type string `json:"type"`
	Data string `json:"data"`
}
