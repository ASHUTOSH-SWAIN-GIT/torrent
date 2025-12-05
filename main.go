package main

import (
	"fmt"
	"os"

	"torrent.ashutosh.net/internal/peer"
	"torrent.ashutosh.net/internal/tracker"
)

func main() {

	data, err := os.ReadFile("debian-13.2.0-amd64-netinst.iso.torrent")
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

	peers, err := tracker.Announce(announceURL)
	if err != nil {
		panic(err)
	}

	fmt.Println("\nPeers returned by tracker:")
	for _, p := range peers {
		fmt.Println(p.IP, p.Port)
	}

	fmt.Println("\n Testing handshake")

	for _, p := range peers {
		conn, err := peer.ConnectAndHandshake(p, torrent.InfoHash, peerID)
		if err != nil {
			fmt.Println("Failed handshake with", p.IP, p.Port, "error:", err)
			continue
		}

		fmt.Println("Handshake successful with", p.IP, p.Port)
		conn.Close()
		break
	}
}
