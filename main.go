package main

import (
	"fmt"
	"os"

	"torrent.ashutosh.net/internal/downloader"
	"torrent.ashutosh.net/internal/tracker"
)

func main() {

	// load torrent file
	data, err := os.ReadFile("cab4A8WPHeOLLZvbDYW4wySxq4CNFexA1KHNoT8O.torrent")
	if err != nil {
		panic(err)
	}

	// parse torrent metadata
	t, err := tracker.ParseTorrent(data)
	if err != nil {
		panic(err)
	}

	fmt.Println("Announce:", t.Announce)
	fmt.Println("Info Hash:", t.InfoHash)
	fmt.Println("Name:", t.Info.Name)
	fmt.Println("Piece Length:", t.Info.PieceLength)
	fmt.Println("Total Length:", t.Info.Length)
	fmt.Println("Total Pieces:", t.NumPieces)

	// generate peer id
	peerID := tracker.GeneratePeerID()
	fmt.Println("Peer ID:", string(peerID[:]))

	// build announce url
	announceURL, err := tracker.BuildAnnounceURL(t, peerID, 6881)
	if err != nil {
		panic(err)
	}
	fmt.Println("Announce URL:", announceURL)

	// request tracker for peers
	peers, err := tracker.Announce(announceURL, t, peerID, 6881)
	if err != nil {
		panic(err)
	}

	if len(peers) == 0 {
		fmt.Println("Tracker returned no peers")
		return
	}

	fmt.Println("\nPeers returned by tracker:")
	for _, p := range peers {
		fmt.Println(p.IP, p.Port)
	}

	// create download session
	outputPath := "downloaded_" + t.Info.Name
	sess, err := downloader.NewSession(t, peerID, outputPath)
	if err != nil {
		panic(err)
	}

	fmt.Println("\nStarting multi peer download for", t.Info.Name)

	// start downloading all pieces
	if err := sess.DownloadAll(peers); err != nil {
		fmt.Println("Download failed:", err)
		return
	}

	fmt.Println("\nDownload completed successfully. Saved to:", outputPath)
}
