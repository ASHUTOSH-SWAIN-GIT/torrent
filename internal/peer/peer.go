package peer

import (
	"fmt"
	"io"
	"net"
	"time"

	"torrent.ashutosh.net/internal/tracker"
)

type Handshake struct {
	Pstr     string
	InfoHash [20]byte
	PeerID   [20]byte
}

func NewHandshake(infoHash [20]byte, peerID [20]byte) *Handshake {
	return &Handshake{
		Pstr:     "BitTorrent protocol", // FIXED: correct protocol string
		InfoHash: infoHash,
		PeerID:   peerID,
	}
}

func (h *Handshake) Serialize() []byte {
	pstr := []byte(h.Pstr)
	buf := make([]byte, 49+len(pstr))

	buf[0] = byte(len(pstr))
	copy(buf[1:], pstr)
	// reserved bytes are zero by default
	offset := 1 + len(pstr) + 8

	copy(buf[offset:], h.InfoHash[:])
	offset += 20

	copy(buf[offset:], h.PeerID[:])

	return buf
}

func ReadHandshake(r io.Reader) (*Handshake, error) {
	// read pstrlen
	pstrlen := make([]byte, 1)
	if _, err := io.ReadFull(r, pstrlen); err != nil {
		return nil, err
	}

	// read pstr
	pstr := make([]byte, pstrlen[0])
	if _, err := io.ReadFull(r, pstr); err != nil {
		return nil, err
	}

	if string(pstr) != "BitTorrent protocol" {
		return nil, fmt.Errorf("unexpected protocol string: %s", string(pstr))
	}

	// read reserved
	reserved := make([]byte, 8)
	if _, err := io.ReadFull(r, reserved); err != nil {
		return nil, err
	}

	infoHash := make([]byte, 20)
	if _, err := io.ReadFull(r, infoHash); err != nil {
		return nil, err
	}

	peerID := make([]byte, 20)
	if _, err := io.ReadFull(r, peerID); err != nil {
		return nil, err
	}

	var ih [20]byte
	var pid [20]byte
	copy(ih[:], infoHash)
	copy(pid[:], peerID)

	return &Handshake{
		Pstr:     string(pstr),
		InfoHash: ih,
		PeerID:   pid,
	}, nil
}

func ConnectAndHandshake(p tracker.Peer, infoHash [20]byte, peerID [20]byte) (net.Conn, error) {
	addr := fmt.Sprintf("%s:%d", p.IP.String(), p.Port)

	// Increase connection timeout to 5 seconds
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		return nil, err
	}

	// Set default read/write timeouts for the connection
	conn.SetDeadline(time.Now().Add(10 * time.Second))

	// send handshake
	hs := NewHandshake(infoHash, peerID)

	conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	_, err = conn.Write(hs.Serialize())
	if err != nil {
		conn.Close()
		return nil, err
	}

	// read handshake
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	peerHS, err := ReadHandshake(conn)
	if err != nil {
		conn.Close()
		return nil, err
	}

	if peerHS.InfoHash != infoHash {
		conn.Close()
		return nil, fmt.Errorf("info hash mismatch")
	}

	// Clear deadline after successful handshake - will be set per operation
	conn.SetDeadline(time.Time{})

	return conn, nil
}
