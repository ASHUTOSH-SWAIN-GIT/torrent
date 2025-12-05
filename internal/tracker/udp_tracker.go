package tracker

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math/rand/v2"
	"net"
	"time"
)

const (
	actionConnect  = 0
	actionAnnounce = 1
)

func udpAnnounce(rawURL string, infoHash [20]byte, peerID [20]byte, port int, left int) ([]Peer, error) {
	addr, err := net.ResolveUDPAddr("udp", extractHostPort(rawURL))
	if err != nil {
		return nil, err
	}

	conn, err := net.DialUDP("udp", nil, addr)
	if err != nil {
		return nil, err
	}

	defer conn.Close()

	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))

	// connect  to the request
	transactionID := rand.Uint32()
	buf := new(bytes.Buffer)

	binary.Write(buf, binary.BigEndian, uint64(0x41727101980)) // protocol id
	binary.Write(buf, binary.BigEndian, uint32(actionConnect))
	binary.Write(buf, binary.BigEndian, transactionID)

	_, err = conn.Write(buf.Bytes())
	if err != nil {
		return nil, err
	}

	// receive the response
	resp := make([]byte, 1024)
	n, _, err := conn.ReadFromUDP(resp)
	if err != nil {
		return nil, err
	}

	if n < 16 {
		return nil, errors.New("Invalid  connect response ")
	}

	action := binary.BigEndian.Uint32(resp[0:4])
	respTx := binary.BigEndian.Uint32(resp[4:8])
	if action != actionConnect || respTx != transactionID {
		return nil, errors.New("transaction mismatch in connect")
	}

	connectionID := resp[8:16]

	// send announce request
	transactionID = rand.Uint32()
	buf.Reset()

	binary.Write(buf, binary.BigEndian, connectionID)
	binary.Write(buf, binary.BigEndian, uint32(actionAnnounce))
	binary.Write(buf, binary.BigEndian, transactionID)
	binary.Write(buf, binary.BigEndian, infoHash)
	binary.Write(buf, binary.BigEndian, peerID)
	binary.Write(buf, binary.BigEndian, uint64(0))
	binary.Write(buf, binary.BigEndian, uint64(left))
	binary.Write(buf, binary.BigEndian, uint64(0))
	binary.Write(buf, binary.BigEndian, uint32(0))
	binary.Write(buf, binary.BigEndian, uint32(0))
	binary.Write(buf, binary.BigEndian, rand.Uint32())
	binary.Write(buf, binary.BigEndian, int32(-1))
	binary.Write(buf, binary.BigEndian, uint16(port))
	binary.Write(buf, binary.BigEndian, uint16(0))

	_, err = conn.Write(buf.Bytes())
	if err != nil {
		return nil, err
	}

	// receive the announce response
	n, _, err = conn.ReadFromUDP(resp)
	if err != nil {
		return nil, err
	}

	if n < 20 {
		return nil, errors.New("invalid announce response")
	}

	action = binary.BigEndian.Uint32(resp[0:4])
	respTx = binary.BigEndian.Uint32(resp[4:8])
	if action != actionAnnounce || respTx != transactionID {
		return nil, errors.New("transaction mismatch in announce")
	}

	peersBin := resp[20:n]
	return parseCompactPeers(peersBin)
}

func extractHostPort(url string) string {
	url = url[len("udp://"):]
	i := bytes.IndexByte([]byte(url), '/')
	if i == -1 {
		return url
	}

	return url[:i]

}
