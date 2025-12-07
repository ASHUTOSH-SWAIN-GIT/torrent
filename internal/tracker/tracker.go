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
	"time"

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

	var announceList [][]string
	if al, ok := decoded["announce-list"]; ok {
		if tiers, ok := al.([]interface{}); ok {
			for _, tierVal := range tiers {
				tierInterfaces, ok := tierVal.([]interface{})
				if !ok {
					continue
				}
				var tier []string
				for _, trackerVal := range tierInterfaces {
					if b, ok := trackerVal.([]byte); ok {
						tier = append(tier, string(b))
					}
				}
				if len(tier) > 0 {
					announceList = append(announceList, tier)
				}
			}
		}
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

	// Length - check single file first, then multi-file
	length := 0
	if lengthVal, ok := infoMap["length"]; ok && lengthVal != nil {
		// Single file torrent
		lengthInt, ok := lengthVal.(int64)
		if !ok {
			return nil, fmt.Errorf("'length' field is not an integer")
		}
		length = int(lengthInt)
	} else if filesVal, ok := infoMap["files"]; ok {
		// Multi-file torrent - sum all file lengths
		files, ok := filesVal.([]interface{})
		if ok {
			for _, f := range files {
				fileDict, ok := f.(map[string]interface{})
				if !ok {
					continue
				}
				if fileLengthVal, ok := fileDict["length"]; ok {
					if fileLength, ok := fileLengthVal.(int64); ok {
						length += int(fileLength)
					}
				}
			}
		}
	}

	t := bencode.Torrent{
		Announce:     string(announce),
		AnnounceList: announceList,
		InfoHash:     infoHash,
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

// GetAllTrackers returns all tracker URLs from the torrent (primary + announce-list)
func GetAllTrackers(t *bencode.Torrent) []string {
	var trackers []string
	seen := make(map[string]bool)

	// Add primary tracker
	if t.Announce != "" && !seen[t.Announce] {
		trackers = append(trackers, t.Announce)
		seen[t.Announce] = true
	}

	// Add announce-list trackers (deduplicated)
	for _, tier := range t.AnnounceList {
		for _, trackerURL := range tier {
			if trackerURL != "" && !seen[trackerURL] {
				trackers = append(trackers, trackerURL)
				seen[trackerURL] = true
			}
		}
	}

	return trackers
}

// BuildAnnounceURLFromString builds announce URL for a specific tracker URL
func BuildAnnounceURLFromString(trackerURL string, t *bencode.Torrent, peerID [20]byte, port int) (string, error) {
	u, err := url.Parse(trackerURL)
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

// AnnounceAll queries all trackers concurrently and returns combined peer list
func AnnounceAll(t *bencode.Torrent, peerID [20]byte, port int) ([]Peer, error) {
	trackers := GetAllTrackers(t)
	if len(trackers) == 0 {
		return nil, fmt.Errorf("no trackers found in torrent")
	}

	// Silently query trackers

	type result struct {
		peers   []Peer
		err     error
		tracker string
	}

	resultCh := make(chan result, len(trackers))

	// Query all trackers concurrently
	for _, trackerURL := range trackers {
		go func(url string) {
			// Skip invalid URLs silently
			if url == "" {
				resultCh <- result{nil, fmt.Errorf("empty tracker URL"), url}
				return
			}

			announceURL, err := BuildAnnounceURLFromString(url, t, peerID, port)
			if err != nil {
				resultCh <- result{nil, err, url}
				return
			}

			peers, err := Announce(announceURL, t, peerID, port)
			resultCh <- result{peers, err, url}
		}(trackerURL)
	}

	// Collect results with timeout
	allPeers := make(map[string]Peer) // Use map to deduplicate peers
	successCount := 0
	failedCount := 0

	timeout := time.After(30 * time.Second) // Overall timeout for all trackers

	for i := 0; i < len(trackers); i++ {
		select {
		case res := <-resultCh:
			if res.err != nil {
				failedCount++
				// Silently continue on error
				continue
			}

			successCount++
			// Add unique peers
			for _, peer := range res.peers {
				key := fmt.Sprintf("%s:%d", peer.IP.String(), peer.Port)
				allPeers[key] = peer
			}

		case <-timeout:
			// Some trackers are taking too long, continue with what we have
			// Drain remaining results quickly
			remaining := len(trackers) - i - 1
			for j := 0; j < remaining; j++ {
				select {
				case res := <-resultCh:
					if res.err == nil {
						for _, peer := range res.peers {
							key := fmt.Sprintf("%s:%d", peer.IP.String(), peer.Port)
							allPeers[key] = peer
						}
						successCount++
					} else {
						failedCount++
					}
				case <-time.After(2 * time.Second):
					// Give up on remaining trackers
					goto done
				}
			}
			goto done
		}
	}

done:

	// Convert map to slice
	uniquePeers := make([]Peer, 0, len(allPeers))
	for _, peer := range allPeers {
		uniquePeers = append(uniquePeers, peer)
	}

	// Log peer count from trackers
	fmt.Printf("Peers from trackers: %d unique peers (from %d/%d successful trackers)\n",
		len(uniquePeers), successCount, len(trackers))

	if len(uniquePeers) == 0 {
		return nil, fmt.Errorf("all trackers failed or returned no peers")
	}

	return uniquePeers, nil
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
	if len(rawURL) >= 6 && rawURL[:6] == "udp://" {
		return udpAnnounce(rawURL, t.InfoHash, peerID, port, t.Info.Length)
	}

	// HTTP/HTTPS tracker - add timeout
	client := &http.Client{
		Timeout: 10 * time.Second,
	}

	resp, err := client.Get(rawURL)
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
