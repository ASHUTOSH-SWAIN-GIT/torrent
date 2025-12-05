package main

import (
	"fmt"
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

	fmt.Println("\nTesting handshake with first 100 peers...\n")

	success := false
	limit := 100
	if len(peers) < limit {
		limit = len(peers)
	}

	for i := 0; i < limit; i++ {
		p := peers[i]
		fmt.Printf("Trying %s:%d ... ", p.IP, p.Port)

		conn, err := peer.ConnectAndHandshake(p, torrent.InfoHash, peerID)
		if err != nil {
			fmt.Println("Failed:", err)
			continue
		}

		fmt.Println("SUCCESS")
		conn.Close()
		success = true
		break
	}

	if !success {
		fmt.Println("\nNo successful handshake in first", limit, "peers")
	}

}
