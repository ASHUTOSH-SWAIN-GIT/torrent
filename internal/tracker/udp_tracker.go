package tracker

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math/rand/v2"
	"net"
	"time"
)

const (
	actionConnect  = 0
	actionAnnounce = 1
)

func udpAnnounce(rawURL string, infoHash [20]byte, peerID [20]byte, port int, left int) ([]Peer, error) {

	// Resolve UDP address
	addr, err := net.ResolveUDPAddr("udp", extractHostPort(rawURL))
	if err != nil {
		return nil, err
	}

	conn, err := net.DialUDP("udp", nil, addr)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	conn.SetDeadline(time.Now().Add(5 * time.Second))

	// connect request
	transactionID := rand.Uint32()
	buf := new(bytes.Buffer)

	// protocol id
	binary.Write(buf, binary.BigEndian, uint64(0x41727101980))
	// action connect
	binary.Write(buf, binary.BigEndian, uint32(actionConnect))
	// tx id
	binary.Write(buf, binary.BigEndian, transactionID)

	// send connect
	_, err = conn.Write(buf.Bytes())
	if err != nil {
		return nil, err
	}

	// receive connect response
	resp := make([]byte, 2048)
	n, _, err := conn.ReadFromUDP(resp)
	if err != nil {
		return nil, err
	}
	if n < 16 {
		return nil, errors.New("invalid connect response size")
	}

	action := binary.BigEndian.Uint32(resp[0:4])
	respTx := binary.BigEndian.Uint32(resp[4:8])

	if action == 3 {
		return nil, fmt.Errorf("tracker error: %s", string(resp[8:n]))
	}

	if action != actionConnect || respTx != transactionID {
		return nil, fmt.Errorf("unexpected connect action %d", action)
	}

	// extract connection id (8 bytes)
	connectionID := make([]byte, 8)
	copy(connectionID, resp[8:16])

	// announce request
	transactionID = rand.Uint32()
	buf.Reset()

	// connection id (write raw bytes)
	buf.Write(connectionID)

	// action announce
	binary.Write(buf, binary.BigEndian, uint32(actionAnnounce))

	// transaction id
	binary.Write(buf, binary.BigEndian, transactionID)

	// info hash (20 bytes)
	buf.Write(infoHash[:])

	// peer id (20 bytes)
	buf.Write(peerID[:])

	// downloaded, left, uploaded
	binary.Write(buf, binary.BigEndian, uint64(0))    // downloaded
	binary.Write(buf, binary.BigEndian, uint64(left)) // left
	binary.Write(buf, binary.BigEndian, uint64(0))    // uploaded

	// event, ip, key
	binary.Write(buf, binary.BigEndian, uint32(0))     // event none
	binary.Write(buf, binary.BigEndian, uint32(0))     // ip 0
	binary.Write(buf, binary.BigEndian, rand.Uint32()) // key

	// num want
	binary.Write(buf, binary.BigEndian, int32(-1))

	// port
	binary.Write(buf, binary.BigEndian, uint16(port))

	// send announce request
	_, err = conn.Write(buf.Bytes())
	if err != nil {
		return nil, err
	}

	//announce response
	n, _, err = conn.ReadFromUDP(resp)
	if err != nil {
		return nil, err
	}

	if n < 20 {
		return nil, errors.New("invalid announce response size")
	}

	action = binary.BigEndian.Uint32(resp[0:4])
	respTx = binary.BigEndian.Uint32(resp[4:8])

	if action == 3 {
		return nil, fmt.Errorf("tracker error: %s", string(resp[8:n]))
	}

	if action != actionAnnounce || respTx != transactionID {
		return nil, fmt.Errorf("unexpected announce action %d", action)
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
