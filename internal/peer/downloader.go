package peer

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"sync"
	"time"
)

const DefaultBlockSize = 128 * 1024 // block size received from the peers
const MaxPipelineRequests = 5       // no of block downloads parallely
const BlockTimeout = 8 * time.Second

func SendRequest(w io.Writer, index int, begin int, length int) error {
	buf := make([]byte, 17)
	binary.BigEndian.PutUint32(buf[0:4], 13)
	buf[4] = MsgRequest
	binary.BigEndian.PutUint32(buf[5:9], uint32(index))
	binary.BigEndian.PutUint32(buf[9:13], uint32(begin))
	binary.BigEndian.PutUint32(buf[13:17], uint32(length))

	_, err := w.Write(buf)
	return err
}

// PieceBuffer stores out-of-order pieces for pipelined downloads
type PieceBuffer struct {
	mu      sync.Mutex
	pieces  map[string]*Message // key: "index:begin"
	maxSize int
}

func NewPieceBuffer(maxSize int) *PieceBuffer {
	return &PieceBuffer{
		pieces:  make(map[string]*Message),
		maxSize: maxSize,
	}
}

func (pb *PieceBuffer) Put(index int, begin int, msg *Message) bool {
	pb.mu.Lock()
	defer pb.mu.Unlock()

	// Don't buffer if full (shouldn't happen with proper pipelining)
	if len(pb.pieces) >= pb.maxSize {
		return false
	}

	key := fmt.Sprintf("%d:%d", index, begin)
	pb.pieces[key] = msg
	return true
}

func (pb *PieceBuffer) Get(index int, begin int) (*Message, bool) {
	pb.mu.Lock()
	defer pb.mu.Unlock()

	key := fmt.Sprintf("%d:%d", index, begin)
	msg, ok := pb.pieces[key]
	if ok {
		delete(pb.pieces, key)
	}
	return msg, ok
}

func (pb *PieceBuffer) Clear() {
	pb.mu.Lock()
	defer pb.mu.Unlock()
	pb.pieces = make(map[string]*Message)
}

// ReadPiece reads a piece message from the dispatcher or buffer
func ReadPiece(dispatcher *MessageDispatcher, buffer *PieceBuffer, expectedIndex int, expectedBegin int, timeout time.Duration) (index int, begin int, block []byte, err error) {
	deadline := time.Now().Add(timeout)

	// First check buffer for out-of-order pieces
	if buffer != nil {
		if msg, ok := buffer.Get(expectedIndex, expectedBegin); ok {
			if len(msg.Payload) >= 8 {
				index = int(binary.BigEndian.Uint32(msg.Payload[0:4]))
				begin = int(binary.BigEndian.Uint32(msg.Payload[4:8]))
				block = msg.Payload[8:]
				return index, begin, block, nil
			}
		}
	}

	for {
		// Check timeout
		if time.Now().After(deadline) {
			return 0, 0, nil, fmt.Errorf("timeout waiting for piece message (piece %d, offset %d)", expectedIndex, expectedBegin)
		}

		select {
		case msg, ok := <-dispatcher.GetPieceChannel():
			if !ok {
				return 0, 0, nil, fmt.Errorf("dispatcher closed")
			}
			if len(msg.Payload) < 8 {
				// Invalid message, skip it
				continue
			}

			index = int(binary.BigEndian.Uint32(msg.Payload[0:4]))
			begin = int(binary.BigEndian.Uint32(msg.Payload[4:8]))
			block = msg.Payload[8:]

			// Check if it matches what we requested
			if index == expectedIndex && begin == expectedBegin {
				return index, begin, block, nil
			}

			// Mismatch - buffer it for later use (pipelining)
			if buffer != nil {
				buffer.Put(index, begin, msg)
			}
			// Continue waiting for the correct piece
			continue

		case choked := <-dispatcher.GetChokeChannel():
			if choked {
				// We got choked, wait for unchoke
				unchoked := false
				for !unchoked {
					select {
					case unchokeState := <-dispatcher.GetChokeChannel():
						if !unchokeState {
							// Unchoked, continue waiting for piece
							unchoked = true
						}
					case err := <-dispatcher.GetErrorChannel():
						return 0, 0, nil, fmt.Errorf("peer choked us: %w", err)
					case <-time.After(time.Until(deadline)):
						return 0, 0, nil, fmt.Errorf("timeout waiting for unchoke")
					}
				}
			}
			// Unchoked, continue waiting for piece
			continue

		case err := <-dispatcher.GetErrorChannel():
			return 0, 0, nil, err

		case <-time.After(time.Until(deadline)):
			return 0, 0, nil, fmt.Errorf("timeout waiting for piece message (piece %d, offset %d)", expectedIndex, expectedBegin)
		}
	}
}

// DownloadBlock sends request and reads matching piece using dispatcher and buffer
func DownloadBlock(conn net.Conn, dispatcher *MessageDispatcher, buffer *PieceBuffer, pieceIndex int, begin int, length int) ([]byte, error) {
	// Send request for the block
	if err := SendRequest(conn, pieceIndex, begin, length); err != nil {
		return nil, err
	}

	// Read piece with reduced timeout for faster failure detection
	_, blkBegin, blkData, err := ReadPiece(dispatcher, buffer, pieceIndex, begin, BlockTimeout)
	if err != nil {
		return nil, err
	}

	if blkBegin != begin {
		return nil, fmt.Errorf("unexpected block offset got %d want %d", blkBegin, begin)
	}

	if len(blkData) != length {
		return nil, fmt.Errorf("unexpected block length got %d want %d", len(blkData), length)
	}

	return blkData, nil
}

// PrepareConnection sends interested and waits for unchoke - call once per connection
func PrepareConnection(conn net.Conn) error {
	conn.SetDeadline(time.Now().Add(30 * time.Second))
	defer conn.SetDeadline(time.Time{})

	if err := SendIntrested(conn); err != nil {
		return err
	}

	return WaitForUnchoke(conn)
}

// WaitForUnchokeDispatcher waits for unchoke message using dispatcher
func WaitForUnchokeDispatcher(dispatcher *MessageDispatcher, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)

	for {
		if time.Now().After(deadline) {
			return fmt.Errorf("timeout waiting for unchoke")
		}

		select {
		case choked := <-dispatcher.GetChokeChannel():
			if !choked {
				// Unchoked!
				return nil
			}
			// Choked, wait for unchoke
			select {
			case unchoked := <-dispatcher.GetChokeChannel():
				if !unchoked {
					return nil
				}
			case err := <-dispatcher.GetErrorChannel():
				return fmt.Errorf("peer choked us: %w", err)
			case <-time.After(time.Until(deadline)):
				return fmt.Errorf("timeout waiting for unchoke")
			}

		case err := <-dispatcher.GetErrorChannel():
			return err

		case <-time.After(time.Until(deadline)):
			return fmt.Errorf("timeout waiting for unchoke")
		}
	}
}

// DownloadPiece downloads a torrent piece using pipelined block requests
func DownloadPiece(conn net.Conn, dispatcher *MessageDispatcher, pieceIndex int, pieceLength int) ([]byte, error) {
	buf := make([]byte, pieceLength)

	// Create buffer for out-of-order pieces
	buffer := NewPieceBuffer(MaxPipelineRequests * 2)
	defer buffer.Clear()

	// Check cached unchoke state first (fast path)
	if !dispatcher.IsUnchoked() {
		// Wait for unchoke with reduced timeout
		unchokeTimeout := 5 * time.Second
		if err := WaitForUnchokeDispatcher(dispatcher, unchokeTimeout); err != nil {
			return nil, err
		}
	}

	// Calculate all blocks we need
	type blockRequest struct {
		offset int
		size   int
	}
	var blocks []blockRequest
	offset := 0
	for offset < pieceLength {
		blockSize := DefaultBlockSize
		if offset+blockSize > pieceLength {
			blockSize = pieceLength - offset
		}
		blocks = append(blocks, blockRequest{offset: offset, size: blockSize})
		offset += blockSize
	}

	// Download blocks with pipelining
	// Request multiple blocks in parallel, process as they arrive
	pending := make(map[int][]byte) // offset -> block data
	nextOffset := 0                 // Next offset we need to write
	requested := 0                  // Number of blocks requested
	completed := 0                  // Number of blocks completed

	// Request initial batch of blocks (pipelining)
	maxPending := MaxPipelineRequests
	if maxPending > len(blocks) {
		maxPending = len(blocks)
	}

	for requested < maxPending && requested < len(blocks) {
		block := blocks[requested]
		// Send request (non-blocking)
		if err := SendRequest(conn, pieceIndex, block.offset, block.size); err != nil {
			return nil, err
		}
		requested++
	}

	// Process responses as they arrive (can be out of order)
	// Use a map to track which blocks we're waiting for
	waitingFor := make(map[int]bool) // offset -> waiting
	for _, block := range blocks {
		waitingFor[block.offset] = true
	}

	for completed < len(blocks) {
		// Read ANY piece that arrives (pipelining allows out-of-order)
		// We'll match it to the block we need
		var receivedOffset int
		var receivedData []byte

		// Try to get from buffer first (out-of-order pieces)
		foundInBuffer := false
		for offset := range waitingFor {
			if msg, ok := buffer.Get(pieceIndex, offset); ok {
				if len(msg.Payload) >= 8 {
					receivedOffset = int(binary.BigEndian.Uint32(msg.Payload[4:8]))
					receivedData = msg.Payload[8:]
					foundInBuffer = true
					break
				}
			}
		}

		if !foundInBuffer {
			// Read from dispatcher - accept any block we're waiting for
			// Use timeout per block, but allow multiple blocks to arrive
			deadline := time.Now().Add(BlockTimeout)
			received := false

			for !received && time.Now().Before(deadline) {
				select {
				case msg, ok := <-dispatcher.GetPieceChannel():
					if !ok {
						return nil, fmt.Errorf("dispatcher closed")
					}
					if len(msg.Payload) < 8 {
						continue
					}

					msgIndex := int(binary.BigEndian.Uint32(msg.Payload[0:4]))
					msgOffset := int(binary.BigEndian.Uint32(msg.Payload[4:8]))
					msgData := msg.Payload[8:]

					// Check if this is a block we're waiting for
					if msgIndex == pieceIndex && waitingFor[msgOffset] {
						receivedOffset = msgOffset
						receivedData = msgData
						received = true
					} else if msgIndex == pieceIndex {
						// Wrong offset but same piece - buffer it for later
						buffer.Put(msgIndex, msgOffset, msg)
					}
					// Otherwise ignore (wrong piece - might be from different piece download)

				case choked := <-dispatcher.GetChokeChannel():
					if choked {
						// Got choked, wait for unchoke (but don't wait too long)
						unchokeDeadline := time.Now().Add(3 * time.Second)
						unchoked := false
						for !unchoked && time.Now().Before(unchokeDeadline) {
							select {
							case unchokeState := <-dispatcher.GetChokeChannel():
								if !unchokeState {
									unchoked = true
								}
							case <-time.After(time.Until(unchokeDeadline)):
								return nil, fmt.Errorf("timeout waiting for unchoke")
							}
						}
						if !unchoked {
							return nil, fmt.Errorf("timeout waiting for unchoke")
						}
					}

				case err := <-dispatcher.GetErrorChannel():
					return nil, err

				case <-time.After(100 * time.Millisecond):
					// Short timeout to check if we should continue
					// This allows us to check buffer again
					if time.Now().After(deadline) {
						// Real timeout - return error for first waiting block
						for offset := range waitingFor {
							return nil, fmt.Errorf("timeout waiting for block at offset %d", offset)
						}
						return nil, fmt.Errorf("timeout waiting for piece blocks")
					}
					// Check buffer again (might have received out-of-order piece)
					for offset := range waitingFor {
						if msg, ok := buffer.Get(pieceIndex, offset); ok {
							if len(msg.Payload) >= 8 {
								receivedOffset = int(binary.BigEndian.Uint32(msg.Payload[4:8]))
								receivedData = msg.Payload[8:]
								received = true
								break
							}
						}
					}
				}
			}

			if !received {
				// Final check of buffer before giving up
				for offset := range waitingFor {
					if msg, ok := buffer.Get(pieceIndex, offset); ok {
						if len(msg.Payload) >= 8 {
							receivedOffset = int(binary.BigEndian.Uint32(msg.Payload[4:8]))
							receivedData = msg.Payload[8:]
							received = true
							break
						}
					}
				}
				if !received {
					// Return error for first waiting block
					for offset := range waitingFor {
						return nil, fmt.Errorf("timeout waiting for block at offset %d", offset)
					}
					return nil, fmt.Errorf("timeout waiting for piece blocks")
				}
			}
		}

		// Find the block this data belongs to
		var block blockRequest
		foundBlock := false
		for _, b := range blocks {
			if b.offset == receivedOffset {
				block = b
				foundBlock = true
				break
			}
		}

		if !foundBlock {
			continue // Shouldn't happen, but skip if it does
		}

		if len(receivedData) != block.size {
			return nil, fmt.Errorf("unexpected block length got %d want %d", len(receivedData), block.size)
		}

		// Store block (may be out of order)
		pending[block.offset] = receivedData
		delete(waitingFor, block.offset)
		completed++

		// Write blocks in order as they become available
		for nextOffset < pieceLength {
			if blockData, ok := pending[nextOffset]; ok {
				blockSize := DefaultBlockSize
				if nextOffset+blockSize > pieceLength {
					blockSize = pieceLength - nextOffset
				}
				copy(buf[nextOffset:], blockData)
				delete(pending, nextOffset)
				nextOffset += blockSize
			} else {
				break // Wait for next block
			}
		}

		// Request next block to maintain pipeline
		if requested < len(blocks) {
			nextBlock := blocks[requested]
			if err := SendRequest(conn, pieceIndex, nextBlock.offset, nextBlock.size); err != nil {
				return nil, err
			}
			waitingFor[nextBlock.offset] = true
			requested++
		}
	}

	return buf, nil
}

// HandleUploadRequests listens for Request messages and serves pieces using dispatcher
func HandleUploadRequests(conn net.Conn, dispatcher *MessageDispatcher, getPieceData func(int) ([]byte, error), hasPiece func(int) bool) error {
	// Send unchoke to allow peer to download from us
	if err := SendUnchoke(conn); err != nil {
		return err
	}

	for {
		select {
		case msg, ok := <-dispatcher.GetRequestChannel():
			if !ok {
				// Channel closed
				return nil
			}

			// Parse the request from the peer
			if len(msg.Payload) < 12 {
				continue
			}

			reqIndex := int(binary.BigEndian.Uint32(msg.Payload[0:4]))
			reqBegin := int(binary.BigEndian.Uint32(msg.Payload[4:8]))
			reqLength := int(binary.BigEndian.Uint32(msg.Payload[8:12]))

			// Serve the piece if we have it
			if hasPiece(reqIndex) {
				pieceData, err := getPieceData(reqIndex)
				if err != nil {
					continue
				}

				if reqBegin+reqLength > len(pieceData) {
					continue
				}

				// Send the piece block
				block := pieceData[reqBegin : reqBegin+reqLength]
				if err := SendPiece(conn, reqIndex, reqBegin, block); err != nil {
					return err
				}
			}

		case err := <-dispatcher.GetErrorChannel():
			return err

		case <-time.After(30 * time.Second):
			// Timeout - continue listening (keepalive check)
			continue
		}
	}
}
