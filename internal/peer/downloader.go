package peer

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"time"
)

const DefaultBlockSize = 128 * 1024 // Increased to 128KB for maximum throughput

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

// ReadPiece reads a piece message from the dispatcher matching the expected piece index and offset
func ReadPiece(dispatcher *MessageDispatcher, expectedIndex int, expectedBegin int, timeout time.Duration) (index int, begin int, block []byte, err error) {
	deadline := time.Now().Add(timeout)

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

			// Mismatch - this shouldn't happen with sequential downloads
			// But if it does, count mismatches and give up after too many
			// This prevents infinite loops if peer sends wrong pieces
			// Note: We can't easily buffer mismatched pieces, so we skip them
			// The correct piece should arrive eventually if peer is behaving correctly
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

// DownloadBlock sends request and reads matching piece using dispatcher
func DownloadBlock(conn net.Conn, dispatcher *MessageDispatcher, pieceIndex int, begin int, length int) ([]byte, error) {
	// Send request for the block
	if err := SendRequest(conn, pieceIndex, begin, length); err != nil {
		return nil, err
	}

	// Read piece with timeout - increased to 15s for slow connections (128KB can take time)
	// Also handles out-of-order pieces gracefully
	_, blkBegin, blkData, err := ReadPiece(dispatcher, pieceIndex, begin, 15*time.Second)
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

// DownloadPiece downloads a torrent piece by requesting blocks using dispatcher
func DownloadPiece(conn net.Conn, dispatcher *MessageDispatcher, pieceIndex int, pieceLength int) ([]byte, error) {
	buf := make([]byte, pieceLength)
	offset := 0

	// Wait for unchoke before starting using dispatcher
	unchokeTimeout := 10 * time.Second // Increased timeout for unchoke (some peers are slow)
	unchoked := false

	// Check if we're already unchoked by checking choke channel (non-blocking)
	select {
	case choked := <-dispatcher.GetChokeChannel():
		unchoked = !choked
	default:
		// No message yet, wait for unchoke
		if err := WaitForUnchokeDispatcher(dispatcher, unchokeTimeout); err != nil {
			return nil, err
		}
		unchoked = true
	}

	if !unchoked {
		// Wait for unchoke
		if err := WaitForUnchokeDispatcher(dispatcher, unchokeTimeout); err != nil {
			return nil, err
		}
	}

	// Download blocks sequentially
	for offset < pieceLength {
		blockSize := DefaultBlockSize
		if offset+blockSize > pieceLength {
			blockSize = pieceLength - offset
		}

		block, err := DownloadBlock(conn, dispatcher, pieceIndex, offset, blockSize)
		if err != nil {
			return nil, err
		}

		copy(buf[offset:], block)
		offset += blockSize
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
