package tracker

import (
	"crypto/rand"
	"crypto/sha1"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"

	"torrent.ashutosh.net/internal/bencode"
)

type Peer struct {
	IP   net.IP
	Port uint16
}

// loads torrent file  according to the bencode logic
func ParseTorrent(data []byte) (*bencode.Torrent, error) {
	decoded, err := bencode.Unmarshal(data)
	if err != nil {
		return nil, err
	}

	infoVal, ok := decoded["info"]
	if !ok {
		return nil, fmt.Errorf("torrent file missing 'info' field")
	}
	infoMap, ok := infoVal.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("'info' field is not a dictionary")
	}

	infoBytes, err := bencode.Marshal(infoMap)
	if err != nil {
		return nil, err
	}

	infoHash := sha1.Sum(infoBytes)

	announceVal, ok := decoded["announce"]
	if !ok {
		return nil, fmt.Errorf("torrent file missing 'announce' field")
	}
	announce, ok := announceVal.([]byte)
	if !ok {
		return nil, fmt.Errorf("'announce' field is not a string")
	}

	nameVal, ok := infoMap["name"]
	if !ok {
		return nil, fmt.Errorf("torrent file missing 'name' field in info")
	}
	name, ok := nameVal.([]byte)
	if !ok {
		return nil, fmt.Errorf("'name' field is not a string")
	}

	pieceLengthVal, ok := infoMap["piece length"]
	if !ok {
		return nil, fmt.Errorf("torrent file missing 'piece length' field in info")
	}
	pieceLength, ok := pieceLengthVal.(int64)
	if !ok {
		return nil, fmt.Errorf("'piece length' field is not an integer")
	}

	piecesVal, ok := infoMap["pieces"]
	if !ok {
		return nil, fmt.Errorf("torrent file missing 'pieces' field in info")
	}
	pieces, ok := piecesVal.([]byte)
	if !ok {
		return nil, fmt.Errorf("'pieces' field is not a string")
	}

	// Length is optional (for multi-file torrents)
	length := 0
	if lengthVal, ok := infoMap["length"]; ok && lengthVal != nil {
		lengthInt, ok := lengthVal.(int64)
		if !ok {
			return nil, fmt.Errorf("'length' field is not an integer")
		}
		length = int(lengthInt)
	}

	t := bencode.Torrent{
		Announce: string(announce),
		InfoHash: infoHash,
		Info: bencode.Info{
			Name:        string(name),
			PieceLength: int(pieceLength),
			Pieces:      pieces,
			Length:      length,
		},
	}

	t.NumPieces = len(t.Info.Pieces) / 20

	return &t, nil
}

// generating peer  id
func GeneratePeerID() [20]byte {
	var id [20]byte
	copy(id[:], []byte("-GO1000-"))
	rand.Read(id[8:])
	return id
}

// build the url to connect to the tracker
func BuildAnnounceURL(t *bencode.Torrent, peerID [20]byte, port int) (string, error) {
	u, err := url.Parse(t.Announce)
	if err != nil {
		return "", err
	}

	v := url.Values{}
	v.Set("info_hash", string(t.InfoHash[:]))
	v.Set("peer_id", string(peerID[:]))
	v.Set("port", fmt.Sprintf("%d", port))
	v.Set("uploaded", "0")
	v.Set("downloaded", "0")
	v.Set("left", fmt.Sprintf("%d", t.Info.Length))
	v.Set("compact", "1")

	u.RawQuery = v.Encode()
	return u.String(), nil
}

// send request to the tracker
func Announce(rawURL string, t *bencode.Torrent, peerID [20]byte, port int) ([]Peer, error) {

	// detect if udp
	if len(rawURL) >= 4 && rawURL[:6] == "udp://" {
		return udpAnnounce(rawURL, t.InfoHash, peerID, port, t.Info.Length)
	}
	// otherwise fall back to  the http one
	resp, err := http.Get(rawURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	decoded, err := bencode.Unmarshal(body)
	if err != nil {
		return nil, err
	}

	peersVal, ok := decoded["peers"]
	if !ok {
		return nil, fmt.Errorf("tracker response missing 'peers' field")
	}

	// Handle compact format (binary)
	if peersBin, ok := peersVal.([]byte); ok {
		return parseCompactPeers(peersBin)
	}

	// Handle list format (list of dictionaries)
	if peersList, ok := peersVal.([]interface{}); ok {
		return parseListPeers(peersList)
	}

	return nil, fmt.Errorf("unexpected peers format: %T", peersVal)
}

// return the peer list provided by the tracker
func parseCompactPeers(data []byte) ([]Peer, error) {
	if len(data)%6 != 0 {
		return nil, fmt.Errorf("invalid compact peer list length")
	}

	count := len(data) / 6
	peers := make([]Peer, 0, count)

	for i := 0; i < count; i++ {
		idx := i * 6

		ip := net.IPv4(
			data[idx],
			data[idx+1],
			data[idx+2],
			data[idx+3],
		)

		port := binary.BigEndian.Uint16(data[idx+4 : idx+6])

		peers = append(peers, Peer{
			IP:   ip,
			Port: port,
		})
	}

	return peers, nil
}

func parseListPeers(peersList []interface{}) ([]Peer, error) {
	peers := make([]Peer, 0, len(peersList))
	for _, p := range peersList {
		peerDict, ok := p.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("peer entry is not a dictionary: %T", p)
		}

		ipVal, ok := peerDict["ip"]
		if !ok {
			return nil, fmt.Errorf("peer missing 'ip' field")
		}
		ipStr, ok := ipVal.([]byte)
		if !ok {
			return nil, fmt.Errorf("peer 'ip' is not a string: %T", ipVal)
		}

		portVal, ok := peerDict["port"]
		if !ok {
			return nil, fmt.Errorf("peer missing 'port' field")
		}
		portInt, ok := portVal.(int64)
		if !ok {
			return nil, fmt.Errorf("peer 'port' is not an integer: %T", portVal)
		}

		ip := net.ParseIP(string(ipStr))
		if ip == nil {
			return nil, fmt.Errorf("invalid IP address: %s", string(ipStr))
		}

		peers = append(peers, Peer{
			IP:   ip,
			Port: uint16(portInt),
		})
	}
	return peers, nil
}
