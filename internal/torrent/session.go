package torrent

import (
	"crypto/sha1"
	"fmt"
	"os"

	"torrent.ashutosh.net/internal/peer"
	"torrent.ashutosh.net/internal/tracker"
)

type TorrentSession struct {
	InfoHash    [20]byte
	PieceHashes [][20]byte
	PieceLength int
	TotalLength int
	Peers       []tracker.Peer
	PeerID      [20]byte
	File        *os.File
	Completed   []bool
}

func NewTorrentSession(infoHash [20]byte, pieceHashes [][20]byte, pieceLength int, totalLength int, peers []tracker.Peer, peerID [20]byte, outputPath string) (*TorrentSession, error) {
	file, err := os.Create(outputPath)
	if err != nil {
		return nil, err
	}

	completed := make([]bool, len(pieceHashes))

	return &TorrentSession{
		InfoHash:    infoHash,
		PieceHashes: pieceHashes,
		PieceLength: pieceLength,
		TotalLength: totalLength,
		Peers:       peers,
		PeerID:      peerID,
		File:        file,
		Completed:   completed,
	}, nil
}

// Download every piece  sequentially
func (ts *TorrentSession) DownloadAll() error {
	numPieces := len(ts.PieceHashes)

	for i := 0; i < numPieces; i++ {
		if ts.Completed[i] {
			continue
		}

		fmt.Println("Downloading piece", i)

		p := ts.Peers[0]

		conn, err := peer.ConnectAndHandshake(p, ts.InfoHash, ts.PeerID)
		if err != nil {
			fmt.Println("peer connection failed", err)
			continue
		}

		pieceLen := ts.PieceLength

		if i == numPieces-1 {
			remaining := ts.TotalLength - i*ts.PieceLength
			pieceLen = remaining
		}

		// Create dispatcher for this connection
		dispatcher := peer.NewMessageDispatcher(conn)
		dispatcher.Start()
		defer dispatcher.Close()

		data, err := peer.DownloadPiece(conn, dispatcher, i, pieceLen)
		conn.Close()

		if err != nil {
			fmt.Println("piece download failed: ", err)
		}

		// verify sha1
		expected := ts.PieceHashes[i]
		actual := sha1.Sum(data)

		if actual != expected {
			fmt.Println("piece hash mismatch discarding..")
			continue
		}

		// write to file
		offset := int64(i * ts.PieceLength)
		if _, err := ts.File.WriteAt(data, offset); err != nil {
			return err
		}

		ts.Completed[i] = true
		fmt.Println("piece", i, "done")

	}

	return nil
}
