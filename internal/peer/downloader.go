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

func ReadPiece(conn net.Conn) (index int, begin int, block []byte, err error) {
	for {
		msg, err := ReadMessage(conn)
		if err != nil {
			return 0, 0, nil, err
		}

		if msg == nil {
			// keepalive ignore
			continue
		}

		switch msg.ID {
		case MsgPiece:
			if len(msg.Payload) < 8 {
				return 0, 0, nil, fmt.Errorf("piece payload too small")
			}

			index = int(binary.BigEndian.Uint32(msg.Payload[0:4]))
			begin = int(binary.BigEndian.Uint32(msg.Payload[4:8]))
			block = msg.Payload[8:]
			return index, begin, block, nil

		case MsgChoke:
			// Wait for unchoke again
			if err := WaitForUnchoke(conn); err != nil {
				return 0, 0, nil, fmt.Errorf("peer choked us: %w", err)
			}
			continue

		case MsgUnchoke:
			// ready now keep reading next message
			continue

		case MsgHave:
			// ignored for now, high level does not track availability yet
			continue

		case MsgBitfield:
			// already read at start, ignore extras for now
			continue

		default:
			continue
		}
	}
}

// sends request for a whole block and reads the matching piece
func DownloadBlock(conn net.Conn, pieceIndex int, begin int, length int) ([]byte, error) {
	// send request for the block
	if err := SendRequest(conn, pieceIndex, begin, length); err != nil {
		return nil, err
	}

	_, blkBegin, blkData, err := ReadPiece(conn)
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

// downloads a torrent piece by requesting blocks
func DownloadPiece(conn net.Conn, pieceIndex int, pieceLength int) ([]byte, error) {
	buf := make([]byte, pieceLength)
	offset := 0

	// Wait for unchoke before starting (peer might have choked us)
	// Set very short timeout - if peer doesn't unchoke quickly, try another peer
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if err := WaitForUnchoke(conn); err != nil {
		return nil, err
	}
	conn.SetReadDeadline(time.Time{})

	// Download blocks sequentially with aggressive timeouts
	// Large blocks (128KB) + short timeouts = fast downloads from good peers, quick failure on bad ones
	for offset < pieceLength {
		blockSize := DefaultBlockSize
		if offset+blockSize > pieceLength {
			blockSize = pieceLength - offset
		}

		// Very short timeout - fail fast on slow peers (3 seconds for 128KB is reasonable)
		conn.SetDeadline(time.Now().Add(3 * time.Second))
		block, err := DownloadBlock(conn, pieceIndex, offset, blockSize)
		if err != nil {
			conn.SetDeadline(time.Time{})
			return nil, err
		}

		copy(buf[offset:], block)
		offset += blockSize
	}

	conn.SetDeadline(time.Time{})

	conn.SetDeadline(time.Time{})
	return buf, nil
}

// listens for pieces from the peers and  serves them , runs concurrently with downloading
func HandleUploadRequests(conn net.Conn, getPieceData func(int) ([]byte, error), hasPiece func(int) bool) error {
	// send unchoke to  allow  peer to download  from us
	if err := SendUnchoke(conn); err != nil {
		return err
	}

	for {
		conn.SetReadDeadline(time.Now().Add(30 * time.Second))
		msg, err := ReadMessage(conn)
		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				conn.SetReadDeadline(time.Time{})
				continue
			}
			return err
		}
		conn.SetReadDeadline(time.Time{})
		if msg == nil {
			continue //keepalive
		}

		switch msg.ID {
		case MsgRequest:
			// parse the request from the peer
			if len(msg.Payload) < 12 {
				continue
			}

			reqIndex := int(binary.BigEndian.Uint32(msg.Payload[0:4]))
			reqBegin := int(binary.BigEndian.Uint32(msg.Payload[4:8]))
			reqLength := int(binary.BigEndian.Uint32(msg.Payload[8:12]))

			// serve the piece if we have that
			if hasPiece(reqIndex) {
				pieceData, err := getPieceData(reqIndex)
				if err != nil {
					continue
				}

				if reqBegin+reqLength > len(pieceData) {
					continue
				}
				// send the piece block
				block := pieceData[reqBegin : reqBegin+reqLength]
				if err := SendPiece(conn, reqIndex, reqBegin, block); err != nil {
					return err
				}
			}
		case MsgChoke:
			// peer choked us we cant download but can upload to them
			continue
		case MsgUnchoke:
			continue
		case MsgInterested:
			continue
		case MsgNotInterested:
			continue
		default:
			continue
		}
	}
}
