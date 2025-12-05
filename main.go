package main

import (
	"crypto/sha1"
	"fmt"
	"net"
	"os"

	"torrent.ashutosh.net/internal/peer"
	"torrent.ashutosh.net/internal/tracker"
)

func main() {

	data, err := os.ReadFile("cab4A8WPHeOLLZvbDYW4wySxq4CNFexA1KHNoT8O.torrent")
	if err != nil {
		panic(err)
	}

	torrent, err := tracker.ParseTorrent(data)
	if err != nil {
		panic(err)
	}

	fmt.Println("Announce:", torrent.Announce)
	fmt.Println("Info Hash:", torrent.InfoHash)

	peerID := tracker.GeneratePeerID()
	fmt.Println("Peer ID:", string(peerID[:]))

	announceURL, err := tracker.BuildAnnounceURL(torrent, peerID, 6881)
	if err != nil {
		panic(err)
	}
	fmt.Println("Announce URL:", announceURL)

	peers, err := tracker.Announce(announceURL, torrent, peerID, 6881)
	if err != nil {
		panic(err)
	}

	fmt.Println("\nPeers returned by tracker:")
	for _, p := range peers {
		fmt.Println(p.IP, p.Port)
	}

	fmt.Println("\nTrying to find one peer for downloading...")

	var conn net.Conn
	var workingPeer tracker.Peer
	found := false

	for _, p := range peers {
		fmt.Printf("Trying %s:%d ... ", p.IP, p.Port)

		c, err := peer.ConnectAndHandshake(p, torrent.InfoHash, peerID)
		if err != nil {
			fmt.Println("Failed:", err)
			continue
		}

		fmt.Println("Connected")
		conn = c
		workingPeer = p
		found = true
		break
	}

	if !found {
		fmt.Println("Could not connect to any peer")
		return
	}

	defer conn.Close()

	fmt.Println("\nConnected to peer:", workingPeer.IP, workingPeer.Port)

	// Download first piece
	pieceIndex := 0
	pieceLength := torrent.Info.PieceLength

	// Handle last piece length
	if pieceIndex == len(torrent.Info.Pieces)/20-1 {
		remaining := torrent.Info.Length - pieceIndex*pieceLength
		pieceLength = remaining
	}

	fmt.Println("Downloading piece", pieceIndex)

	pieceData, err := peer.DownloadPiece(conn, pieceIndex, pieceLength)
	if err != nil {
		fmt.Println("Download failed:", err)
		return
	}

	// Extract expected hash from pieces
	var expectedHash [20]byte
	if pieceIndex*20+20 <= len(torrent.Info.Pieces) {
		copy(expectedHash[:], torrent.Info.Pieces[pieceIndex*20:pieceIndex*20+20])
	} else {
		fmt.Println("Invalid piece index")
		return
	}
	actualHash := sha1.Sum(pieceData)

	if expectedHash != actualHash {
		fmt.Println("Piece verification failed")
		return
	}

	fmt.Println("Piece verified")

	// Save piece to file
	err = os.WriteFile("output_piece.bin", pieceData, 0644)
	if err != nil {
		fmt.Println("Failed to save piece", err)
		return
	}

	fmt.Println("Piece saved as output_piece.bin")
}
