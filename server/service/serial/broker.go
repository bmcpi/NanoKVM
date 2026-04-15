package serial

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"

	"github.com/creack/pty"
	log "github.com/sirupsen/logrus"

	"github.com/tinkerbell-community/NanoKVM/server/config"
)

// Session represents one consumer of the serial port (WebSocket, IPMI SOL, etc.).
type Session struct {
	ID     string
	output io.Writer // receives serial port output
}

// Broker manages a single shared serial port connection, allowing multiple
// concurrent sessions to read from and write to it. Modelled after the
// tinkerbell/secondstar shared-terminal pattern.
//
// Architecture:
//
//	                ┌─── WebSocket session ──► ws.WriteMessage
//	serial port ──► │─── IPMI SOL session  ──► UDP sendData
//	  (picocom)     └─── future session    ──► ...
//	      ▲
//	      │  writes (any session)
//	      └── Write()
type Broker struct {
	mu sync.Mutex

	// sessions tracks connected consumers by ID.
	sessions sync.Map // string → *Session

	// multiwriter fans out serial reads to all sessions.
	mw *MultiWriter

	// stdin writes to the serial port process.
	stdin io.Writer

	// process management
	cmd    *exec.Cmd
	ptmx   *os.File
	active bool
	stopCh chan struct{}

	// sessionCount is an atomic counter for fast len checks and unique ID generation.
	sessionCount atomic.Int32
}

// singleton broker instance
var (
	broker     *Broker
	brokerOnce sync.Once
)

// GetBroker returns the singleton Broker instance.
func GetBroker() *Broker {
	brokerOnce.Do(func() {
		broker = &Broker{
			mw: NewMultiWriter(),
		}
	})
	return broker
}

// Connect registers a new session with the given ID and output writer.
// If this is the first session, the serial port process is started.
// Returns the session for later disconnection.
func (b *Broker) Connect(id string, output io.Writer) (*Session, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	// Check for duplicate session ID.
	if _, loaded := b.sessions.Load(id); loaded {
		return nil, fmt.Errorf("session %q already connected", id)
	}

	// Start the serial port process if not already running.
	if !b.active {
		if err := b.startLocked(); err != nil {
			return nil, fmt.Errorf("start serial: %w", err)
		}
	}

	sess := &Session{ID: id, output: output}
	b.sessions.Store(id, sess)
	b.mw.Add(output)
	b.sessionCount.Add(1)

	log.Infof("serial: session %q connected (%d total)", id, b.sessionCount.Load())
	return sess, nil
}

// Disconnect removes a session. If no sessions remain, the serial port
// process is stopped.
func (b *Broker) Disconnect(id string) {
	b.mu.Lock()
	defer b.mu.Unlock()

	val, loaded := b.sessions.LoadAndDelete(id)
	if !loaded {
		return
	}
	sess := val.(*Session)
	b.mw.Remove(sess.output)
	remaining := b.sessionCount.Add(-1)

	log.Infof("serial: session %q disconnected (%d remaining)", id, remaining)

	if remaining <= 0 {
		b.stopLocked()
	}
}

// Write sends data to the serial port. Safe to call from any goroutine.
func (b *Broker) Write(data []byte) (int, error) {
	b.mu.Lock()
	stdin := b.stdin
	b.mu.Unlock()

	if stdin == nil {
		return 0, fmt.Errorf("serial port not active")
	}
	return stdin.Write(data)
}

// Active reports whether the serial port process is running.
func (b *Broker) Active() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.active
}

// SessionCount returns the number of connected sessions.
func (b *Broker) SessionCount() int {
	return int(b.sessionCount.Load())
}

// Close forcibly shuts down the broker, disconnecting all sessions.
func (b *Broker) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.sessions.Range(func(key, val any) bool {
		sess := val.(*Session)
		b.mw.Remove(sess.output)
		b.sessions.Delete(key)
		return true
	})
	b.sessionCount.Store(0)
	b.stopLocked()
}

// picocomFlowFlag maps config flow-control names to picocom's short form.
func picocomFlowFlag(flow string) string {
	switch flow {
	case "hard", "h":
		return "h"
	case "soft", "xon/xoff", "x":
		return "x"
	default:
		return "n"
	}
}

// picocomParityFlag maps config parity names to picocom's short form.
func picocomParityFlag(parity string) string {
	switch parity {
	case "even", "e":
		return "e"
	case "odd", "o":
		return "o"
	default:
		return "n"
	}
}

// startLocked starts picocom on the configured serial port.
// Caller must hold b.mu.
func (b *Broker) startLocked() error {
	cfg := config.GetInstance()
	device := cfg.Serial.Device
	baudRate := fmt.Sprintf("%d", cfg.Serial.BaudRate)

	args := []string{
		"picocom",
		"--quiet",   // suppress startup banner
		"--noreset", // don't reset port on exit
		"--noinit",  // don't reinitialize port settings
		"--nolock",  // allow shared access (broker manages concurrency)
		"-b", baudRate,
		"--flow", picocomFlowFlag(cfg.Serial.FlowControl),
		"--databits", fmt.Sprintf("%d", cfg.Serial.DataBits),
		"--stopbits", fmt.Sprintf("%d", cfg.Serial.StopBits),
		"--parity", picocomParityFlag(cfg.Serial.Parity),
		"--imap", "lfcrlf",
		"--omap", "crlf",
		device,
	}

	cmd := exec.Command(args[0], args[1:]...)
	ptmx, err := pty.Start(cmd)
	if err != nil {
		return fmt.Errorf("start picocom %s: %w", device, err)
	}

	b.cmd = cmd
	b.ptmx = ptmx
	b.stdin = ptmx
	b.active = true
	b.stopCh = make(chan struct{})

	go b.readLoop()

	log.Infof("serial: started picocom on %s @ %s baud", device, baudRate)
	return nil
}

// stopLocked terminates the serial port process.
// Caller must hold b.mu.
func (b *Broker) stopLocked() {
	if !b.active {
		return
	}

	b.active = false
	close(b.stopCh)

	if b.ptmx != nil {
		_ = b.ptmx.Close()
	}
	if b.cmd != nil && b.cmd.Process != nil {
		_ = b.cmd.Process.Kill()
		_ = b.cmd.Wait()
	}
	b.ptmx = nil
	b.cmd = nil
	b.stdin = nil

	log.Info("serial: stopped")
}

// readLoop reads from the serial port PTY and fans out to all sessions
// via the MultiWriter.
func (b *Broker) readLoop() {
	buf := make([]byte, 4096)

	for {
		select {
		case <-b.stopCh:
			return
		default:
		}

		n, err := b.ptmx.Read(buf)
		if err != nil {
			select {
			case <-b.stopCh:
			default:
				log.Debugf("serial: read error: %s", err)
			}
			return
		}

		if n > 0 {
			// Fan-out to all sessions. MultiWriter.Write is best-effort.
			_, _ = b.mw.Write(buf[:n])
		}
	}
}
