package peer

import (
	"net"
	"sync"
	"time"
)

// MessageDispatcher handles all message reading from a connection
// and routes them to appropriate handlers
type MessageDispatcher struct {
	conn      net.Conn
	pieceCh   chan *Message // For MsgPiece messages (downloads)
	requestCh chan *Message // For MsgRequest messages (uploads)
	chokeCh   chan bool      // For choke/unchoke state changes (true=choked, false=unchoked)
	errCh     chan error     // For read errors
	doneCh    chan struct{}  // For shutdown
	wg        sync.WaitGroup
	mu        sync.Mutex
	closed    bool
	// Cached unchoke state for fast access
	unchokeMu sync.RWMutex
	unchoked  bool // true if we're unchoked
}

// NewMessageDispatcher creates a new message dispatcher for a connection
func NewMessageDispatcher(conn net.Conn) *MessageDispatcher {
	return &MessageDispatcher{
		conn:      conn,
		pieceCh:   make(chan *Message, 50), // Increased buffer to handle out-of-order pieces
		requestCh: make(chan *Message, 10),
		chokeCh:   make(chan bool, 5), // Increased buffer for choke messages
		errCh:     make(chan error, 1),
		doneCh:    make(chan struct{}),
		unchoked:  false, // Assume choked initially
	}
}

// Start begins reading messages from the connection
func (md *MessageDispatcher) Start() {
	md.wg.Add(1)
	go md.readLoop()
}

// readLoop is the single reader for all messages
func (md *MessageDispatcher) readLoop() {
	defer md.wg.Done()
	defer close(md.pieceCh)
	defer close(md.requestCh)
	defer close(md.chokeCh)
	defer close(md.errCh)

	for {
		select {
		case <-md.doneCh:
			return
		default:
		}

		// Set read deadline
		md.conn.SetReadDeadline(time.Now().Add(30 * time.Second))
		msg, err := ReadMessage(md.conn)
		if err != nil {
			// Check if it's a timeout (OK to continue)
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				md.conn.SetReadDeadline(time.Time{})
				continue
			}
			// Send error and exit
			select {
			case md.errCh <- err:
			case <-md.doneCh:
				return
			default:
			}
			return
		}
		md.conn.SetReadDeadline(time.Time{})

		// Route message to appropriate channel
		if msg == nil {
			// Keepalive - ignore
			continue
		}

		switch msg.ID {
		case MsgPiece:
			// Route to download handler
			select {
			case md.pieceCh <- msg:
			case <-md.doneCh:
				return
			}

		case MsgRequest:
			// Route to upload handler
			select {
			case md.requestCh <- msg:
			case <-md.doneCh:
				return
			}

		case MsgChoke:
			// Update cached unchoke state
			md.unchokeMu.Lock()
			md.unchoked = false
			md.unchokeMu.Unlock()
			// Notify about choke state (true = choked)
			select {
			case md.chokeCh <- true:
			case <-md.doneCh:
				return
			default:
				// Channel full, skip
			}

		case MsgUnchoke:
			// Update cached unchoke state
			md.unchokeMu.Lock()
			md.unchoked = true
			md.unchokeMu.Unlock()
			// Notify about unchoke state (false = unchoked)
			select {
			case md.chokeCh <- false:
			case <-md.doneCh:
				return
			default:
				// Channel full, skip
			}

		case MsgHave, MsgBitfield, MsgInterested, MsgNotInterested:
			// These can be ignored or handled separately if needed
			continue

		default:
			// Unknown message type - ignore
			continue
		}
	}
}

// Close shuts down the dispatcher
func (md *MessageDispatcher) Close() {
	md.mu.Lock()
	if md.closed {
		md.mu.Unlock()
		return
	}
	md.closed = true
	close(md.doneCh)
	md.mu.Unlock()
	md.wg.Wait()
}

// GetPieceChannel returns the channel for piece messages
func (md *MessageDispatcher) GetPieceChannel() <-chan *Message {
	return md.pieceCh
}

// GetRequestChannel returns the channel for request messages
func (md *MessageDispatcher) GetRequestChannel() <-chan *Message {
	return md.requestCh
}

// GetChokeChannel returns the channel for choke/unchoke notifications
func (md *MessageDispatcher) GetChokeChannel() <-chan bool {
	return md.chokeCh
}

// GetErrorChannel returns the channel for errors
func (md *MessageDispatcher) GetErrorChannel() <-chan error {
	return md.errCh
}

// IsUnchoked returns the cached unchoke state (thread-safe)
func (md *MessageDispatcher) IsUnchoked() bool {
	md.unchokeMu.RLock()
	defer md.unchokeMu.RUnlock()
	return md.unchoked
}
