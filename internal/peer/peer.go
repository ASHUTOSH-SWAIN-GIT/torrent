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
		Pstr:     "BitTorrent Protocol",
		InfoHash: infoHash,
		PeerID:   peerID,
	}
}

// serialize handshake to bytes
func (h *Handshake) Serialize() []byte {
	buf := make([]byte, 49+len(h.Pstr))

	buf[0] = byte(len(h.Pstr))
	copy(buf[1:], h.Pstr)
	copy(buf[1+len(h.Pstr):], make([]byte, 8))  // reserved
	copy(buf[1+len(h.Pstr)+8:], h.InfoHash[:])  // info hash
	copy(buf[1+len(h.Pstr)+8+20:], h.PeerID[:]) // peer id

	return buf
}

// read handshake status  from the peer side
func ReadHandshake(r io.Reader) (*Handshake, error) {
	pstrlen := make([]byte, 1)
	if _, err := r.Read(pstrlen); err != nil {
		return nil, err
	}

	pstr := make([]byte, pstrlen[0])
	if _, err := io.ReadFull(r, pstr); err != nil {
		return nil, err
	}

	reserved := make([]byte, 8)
	io.ReadFull(r, reserved)

	infoHash := make([]byte, 20)
	io.ReadFull(r, infoHash)

	peerID := make([]byte, 20)
	io.ReadFull(r, peerID)

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

// connect to the peer and complete the handshake
func ConnectAndHandshake(p tracker.Peer, infoHash [20]byte, peerID [20]byte) (net.Conn, error) {
	addr := fmt.Sprintf("%s:%d", p.IP.String(), p.Port)

	conn, err := net.DialTimeout("tcp", addr, 800*time.Millisecond)
	if err != nil {
		return nil, err
	}

	// send handshake
	hs := NewHandshake(infoHash, peerID)

	// deadline for write handshake
	conn.SetWriteDeadline(time.Now().Add(500 * time.Millisecond))

	_, err = conn.Write(hs.Serialize())
	if err != nil {
		conn.Close()
		return nil, err
	}

	// deadline for read handshake
	conn.SetReadDeadline(time.Now().Add(1 * time.Second))

	// read handshake from the peer
	peerHS, err := ReadHandshake(conn)
	if err != nil {
		conn.Close()
		return nil, err
	}

	// remove all the deadlines after succesful handshake
	conn.SetDeadline(time.Time{})

	if peerHS.InfoHash != infoHash {
		conn.Close()
		return nil, fmt.Errorf("info hash mismatch")
	}
	return conn, nil
}
