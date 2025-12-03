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

	infoMap := decoded["info"].(map[string]interface{})

	infoBytes, err := bencode.Marshal(infoMap)
	if err != nil {
		return nil, err
	}

	infoHash := sha1.Sum(infoBytes)

	t := bencode.Torrent{
		Announce: string(decoded["announce"].([]byte)),
		InfoHash: infoHash,
		Info: bencode.Info{
			Name:        string(infoMap["name"].([]byte)),
			PieceLength: int(infoMap["piece length"].(int64)),
			Pieces:      infoMap["pieces"].([]byte),
			Length:      int(infoMap["length"].(int64)),
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
func Announce(url string) ([]Peer, error) {
	resp, err := http.Get(url)
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

	peersBin := decoded["peers"].([]byte)
	return parseCompactPeers(peersBin)
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
