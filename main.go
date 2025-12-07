package main

import (
	"fmt"
	"os"

	"torrent.ashutosh.net/internal/downloader"
	"torrent.ashutosh.net/internal/tracker"
)

func main() {
	// Get torrent file from command line argument
	if len(os.Args) < 2 {
		fmt.Println("Usage: go run main.go <torrent-file>")
		fmt.Println("Example: go run main.go cab4A8WPHeOLLZvbDYW4wySxq4CNFexA1KHNoT8O.torrent")
		os.Exit(1)
	}

	torrentFile := os.Args[1]

	// Load torrent file
	data, err := os.ReadFile(torrentFile)
	if err != nil {
		fmt.Printf("Error reading torrent file '%s': %v\n", torrentFile, err)
		os.Exit(1)
	}

	t, err := tracker.ParseTorrent(data)
	if err != nil {
		panic(err)
	}

	fmt.Println("Main tracker:", t.Announce)
	if len(t.AnnounceList) > 0 {
		fmt.Printf("Additional tracker tiers: %d\n", len(t.AnnounceList))
	}

	peerID := tracker.GeneratePeerID()

	// Query all trackers concurrently and get combined peer list
	allPeers, err := tracker.AnnounceAll(t, peerID, 6881)
	if err != nil {
		fmt.Println("Failed to get peers from trackers:", err)
		return
	}

	if len(allPeers) == 0 {
		fmt.Println("No peers found from any tracker")
		return
	}

	// Start download session
	outputPath := "downloaded_" + t.Info.Name

	sess, err := downloader.NewSession(t, peerID, outputPath)
	if err != nil {
		panic(err)
	}

	fmt.Println("Starting download...")

	if err := sess.DownloadAll(allPeers); err != nil {
		fmt.Println("Download failed:", err)
	} else {
		fmt.Println("Download completed successfully.")
	}
}
