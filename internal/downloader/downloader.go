package downloader

import (
	"crypto/sha1"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"torrent.ashutosh.net/internal/bencode"
	"torrent.ashutosh.net/internal/peer"
	"torrent.ashutosh.net/internal/tracker"
)

type Session struct {
	Torrent    *bencode.Torrent
	PeerID     [20]byte
	OutputPath string

	file *os.File
	mu   sync.Mutex

	// For dynamic peer management
	conns   []net.Conn
	connsMu sync.RWMutex
	port    int
}

// check if error is a fatal connection error
func isConnectionDead(err error) bool {
	if err == nil {
		return false
	}
	errStr := err.Error()
	return strings.Contains(errStr, "broken pipe") ||
		strings.Contains(errStr, "connection reset") ||
		strings.Contains(errStr, "connection refused") ||
		strings.Contains(errStr, "EOF") ||
		strings.Contains(errStr, "use of closed network connection")
}

// helper to get hash for a piece
func pieceHash(t *bencode.Torrent, index int) [20]byte {
	var h [20]byte
	start := index * 20
	copy(h[:], t.Info.Pieces[start:start+20])
	return h
}

// create a downloader session
func NewSession(t *bencode.Torrent, peerID [20]byte, outputPath string) (*Session, error) {
	f, err := os.Create(outputPath)
	if err != nil {
		return nil, err
	}

	s := &Session{
		Torrent:    t,
		PeerID:     peerID,
		OutputPath: outputPath,
		file:       f,
		port:       6881, // Default port
	}

	return s, nil
}

func (s *Session) Close() error {
	if s.file != nil {
		return s.file.Close()
	}
	return nil
}

// gets the piece to send to the peer from the file
func (s *Session) getPieceData(pieceIndex int) ([]byte, error) {
	pieceLen := s.Torrent.Info.PieceLength
	if pieceIndex == s.Torrent.NumPieces-1 {
		totalLen := s.Torrent.Info.Length
		remaining := totalLen - pieceIndex*s.Torrent.Info.PieceLength
		pieceLen = remaining
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	offset := int64(pieceIndex * s.Torrent.Info.PieceLength)
	data := make([]byte, pieceLen)
	_, err := s.file.ReadAt(data, offset)
	return data, err
}

// Helper function to connect to a peer
func (s *Session) connectToPeer(peerAddr tracker.Peer) (net.Conn, error) {
	conn, err := peer.ConnectAndHandshake(peerAddr, s.Torrent.InfoHash, s.PeerID)
	if err != nil {
		return nil, err
	}
	// read optional bitfield so it does not interfere later
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, err = peer.TryReadBitField(conn)
	conn.SetReadDeadline(time.Time{})
	if err != nil {
		conn.Close()
		return nil, err
	}

	// Don't wait for unchoke during connection - do it later per piece
	// Just send interested
	if err := peer.SendIntrested(conn); err != nil {
		conn.Close()
		return nil, err
	}

	return conn, nil
}

// sending message of the piece to all the connected peers
func (s *Session) broadcastHave(pieceIndex int) {
	s.connsMu.RLock()
	conns := make([]net.Conn, len(s.conns))
	copy(conns, s.conns)
	s.connsMu.RUnlock()

	for _, conn := range conns {
		go func(c net.Conn) {
			if err := peer.SendHave(c, pieceIndex); err != nil {

			}
		}(conn)
	}
}

// connectToPeersBatch connects to a batch of peers concurrently
func (s *Session) connectToPeersBatch(peers []tracker.Peer, maxPeers int, existingCount int) []net.Conn {
	if len(peers) == 0 {
		return nil
	}

	var conns []net.Conn
	var connsMu sync.Mutex
	var attempts int32 = 0
	var successes int32 = 0

	// Calculate how many we can still add
	remaining := maxPeers - existingCount
	if remaining <= 0 {
		return nil
	}

	// Try to connect to ALL peers concurrently - no limits
	// The maxPeers limit will be enforced when adding connections
	var wgConn sync.WaitGroup
	// Spawn one goroutine per peer - try ALL peers from all trackers
	for _, p := range peers {
		wgConn.Add(1)
		go func(peerAddr tracker.Peer) {
			defer wgConn.Done()

			// Check if we already have enough connections (early exit optimization)
			connsMu.Lock()
			currentCount := len(conns) + existingCount
			connsMu.Unlock()

			// Stop trying if we have enough connections
			if currentCount >= maxPeers {
				return
			}

			atomic.AddInt32(&attempts, 1)
			conn, err := s.connectToPeer(peerAddr)
			if err != nil {
				// Most peers fail - this is normal (NAT, firewalls, offline)
				return
			}

			// Try to add connection (enforce maxPeers limit here)
			connsMu.Lock()
			if len(conns)+existingCount < maxPeers {
				conns = append(conns, conn)
				atomic.AddInt32(&successes, 1)
			} else {
				// We've reached max, close this connection
				conn.Close()
			}
			connsMu.Unlock()
		}(p)
	}

	wgConn.Wait()

	// Log connection results
	fmt.Printf("Connected peers: %d/%d (attempted %d, success rate: %.1f%%)\n",
		len(conns), len(peers), atomic.LoadInt32(&attempts),
		float64(atomic.LoadInt32(&successes))/float64(atomic.LoadInt32(&attempts))*100)

	return conns
}

// connect to the peers and download the whole file
func (s *Session) DownloadAll(initialPeers []tracker.Peer) error {
	defer s.Close()

	numPieces := s.Torrent.NumPieces
	if numPieces == 0 {
		return fmt.Errorf("torrent has 0 pieces")
	}

	fmt.Println("Total pieces:", numPieces)

	maxPeers := 40 // Maximum connections to keep (increased for better parallelization)

	// Initial connection batch - try to get as many as possible
	fmt.Printf("Attempting to connect to %d peers from trackers...\n", len(initialPeers))
	conns := s.connectToPeersBatch(initialPeers, maxPeers, 0)

	if len(conns) == 0 {
		return fmt.Errorf("could not connect to any peers")
	}

	fmt.Printf("Initial connected peers: %d\n", len(conns))

	// If we got very few peers initially, immediately try to get more
	if len(conns) < 15 {
		// Immediately re-announce to get more peers
		newPeers, err := tracker.AnnounceAll(s.Torrent, s.PeerID, s.port)
		if err == nil && len(newPeers) > 0 {
			additionalConns := s.connectToPeersBatch(newPeers, maxPeers, len(conns))
			conns = append(conns, additionalConns...)
			fmt.Printf("Total connected peers after re-announce: %d\n", len(conns))
		}
	}

	// Store connections in session for dynamic management
	s.conns = conns

	// Track which pieces are downloaded
	downloaded := make([]bool, numPieces)
	var downloadedCount int32 = 0

	// Piece queue - use rarest-first strategy (download rarest pieces first)
	// This helps distribute pieces across the swarm faster
	pieceCh := make(chan int, numPieces*2)

	// For now, just queue pieces in order
	// TODO: Implement rarest-first by tracking which peers have which pieces
	for i := 0; i < numPieces; i++ {
		pieceCh <- i
	}

	var wg sync.WaitGroup
	var errorCount int32 = 0
	var activeWorkers int32 = int32(len(conns))
	lastProgressTime := time.Now()
	progressMu := sync.Mutex{}

	// Helper to count active connections (workers will remove dead ones automatically)
	getActiveConnectionCount := func() int {
		s.connsMu.RLock()
		defer s.connsMu.RUnlock()
		return len(s.conns)
	}

	// Background goroutine to periodically re-announce to trackers and get fresh peers
	// Start with aggressive re-announcing (every 5 seconds) if we have few peers
	reconnectTicker := time.NewTicker(5 * time.Second) // Start aggressive
	defer reconnectTicker.Stop()

	doneCh := make(chan struct{})
	defer close(doneCh)

	go func() {
		for {
			select {
			case <-reconnectTicker.C:
				// Check current active peer count
				currentConnCount := getActiveConnectionCount()

				// Adjust re-announce interval based on peer count
				if currentConnCount >= 20 {
					// We have enough peers, slow down re-announcing
					reconnectTicker.Reset(15 * time.Second)
				} else if currentConnCount < 10 {
					// Very few peers, be very aggressive
					reconnectTicker.Reset(5 * time.Second)
				} else {
					// Moderate peer count
					reconnectTicker.Reset(10 * time.Second)
				}

				// Always re-announce if we're below threshold
				targetPeers := 20
				if currentConnCount < 10 {
					targetPeers = 15 // More aggressive if we're really low
				}
				if currentConnCount < targetPeers {
					// Re-announce to all trackers (silently)
					newPeers, err := tracker.AnnounceAll(s.Torrent, s.PeerID, s.port)
					if err != nil {
						// Silently continue on error
						continue
					}

					if len(newPeers) == 0 {
						continue
					}

					// Connect to new peers
					s.connsMu.RLock()
					existingCount := len(s.conns)
					s.connsMu.RUnlock()

					newConns := s.connectToPeersBatch(newPeers, maxPeers, existingCount)

					if len(newConns) > 0 {
						// Add new connections to the pool
						s.connsMu.Lock()
						s.conns = append(s.conns, newConns...)
						newTotal := len(s.conns)
						s.connsMu.Unlock()

						fmt.Printf("Total connected peers: %d (added %d new)\n", newTotal, len(newConns))

						// Start workers for new connections
						for _, newConn := range newConns {
							wg.Add(1)
							atomic.AddInt32(&activeWorkers, 1)
							go s.worker(newConn, &wg, &activeWorkers, &downloadedCount, &errorCount,
								downloaded, numPieces, pieceCh, &lastProgressTime, &progressMu)
						}
					}
				}

			case <-doneCh:
				return
			}
		}
	}()

	// worker for each connection
	for _, c := range conns {
		wg.Add(1)
		go s.worker(c, &wg, &activeWorkers, &downloadedCount, &errorCount,
			downloaded, numPieces, pieceCh, &lastProgressTime, &progressMu)
	}

	// Wait for all pieces to be downloaded
	// Don't close pieceCh until we're done - workers need it
	for atomic.LoadInt32(&downloadedCount) < int32(numPieces) {
		time.Sleep(1 * time.Second)
	}

	// All pieces downloaded, now wait for workers to finish
	wg.Wait()

	// Final summary
	finalCount := atomic.LoadInt32(&downloadedCount)
	totalErrors := atomic.LoadInt32(&errorCount)

	if finalCount < int32(numPieces) {
		fmt.Printf("\nDownload incomplete: %d/%d pieces (%.1f%%) | Total retries: %d\n",
			finalCount, numPieces, float64(finalCount)/float64(numPieces)*100, totalErrors)
		return fmt.Errorf("download incomplete: got %d/%d pieces", finalCount, numPieces)
	}

	fmt.Printf("\n✓ Download complete! %d/%d pieces (100%%) | Total retries: %d\n",
		finalCount, numPieces, totalErrors)
	return nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// worker function that continuously downloads pieces from a peer connection
func (s *Session) worker(conn net.Conn, wg *sync.WaitGroup, activeWorkers *int32,
	downloadedCount *int32, errorCount *int32, downloaded []bool, numPieces int,
	pieceCh chan int, lastProgressTime *time.Time, progressMu *sync.Mutex) {
	defer wg.Done()
	defer atomic.AddInt32(activeWorkers, -1)
	defer conn.Close()

	// Remove connection from list when worker exits (only on fatal errors)
	// We'll remove it in the error handling, not in defer

	for {
		// Check if all pieces are downloaded
		if atomic.LoadInt32(downloadedCount) >= int32(numPieces) {
			return
		}

		// Get next piece to download
		select {
		case pieceIndex, ok := <-pieceCh:
			if !ok {
				return // Channel closed
			}

			// Skip if already downloaded
			if downloaded[pieceIndex] {
				continue
			}

			// Try to download the piece
			err := s.downloadoOnePieceFromPeer(conn, pieceIndex)
			if err != nil {
				atomic.AddInt32(errorCount, 1)

				// Only exit on fatal connection errors
				if isConnectionDead(err) {
					// Connection is dead, remove from list and exit worker
					s.connsMu.Lock()
					for i, c := range s.conns {
						if c == conn {
							s.conns = append(s.conns[:i], s.conns[i+1:]...)
							break
						}
					}
					s.connsMu.Unlock()
					return
				}

				// For other errors (choke, timeout, etc), put piece back in queue and continue
				// Worker stays alive and tries next piece - connection might still be good
				select {
				case pieceCh <- pieceIndex:
					// Piece requeued, continue to next piece with same peer
					continue
				default:
					// Channel full, wait a bit and try again
					time.Sleep(100 * time.Millisecond)
					continue
				}
			}

			// Mark as downloaded
			if !downloaded[pieceIndex] {
				downloaded[pieceIndex] = true
				newCount := atomic.AddInt32(downloadedCount, 1)

				// Print progress periodically
				progressMu.Lock()
				now := time.Now()
				if now.Sub(*lastProgressTime) > 2*time.Second {
					s.connsMu.RLock()
					activePeers := len(s.conns)
					s.connsMu.RUnlock()

					fmt.Printf("Progress: %d/%d pieces (%.1f%%) | Active peers: %d | Retries: %d\n",
						newCount, numPieces, float64(newCount)/float64(numPieces)*100,
						activePeers, atomic.LoadInt32(errorCount))
					*lastProgressTime = now
				}
				progressMu.Unlock()
			}

		case <-time.After(500 * time.Millisecond):
			// Very short timeout - check if we should continue (faster piece pickup)
			if atomic.LoadInt32(downloadedCount) >= int32(numPieces) {
				return
			}
		}
	}
}

// download a single piece from the peer over one connection
func (s *Session) downloadoOnePieceFromPeer(conn net.Conn, pieceIndex int) error {
	pieceLen := s.Torrent.Info.PieceLength

	if pieceIndex == s.Torrent.NumPieces-1 {
		totalLen := s.Torrent.Info.Length
		remaining := totalLen - pieceIndex*s.Torrent.Info.PieceLength
		pieceLen = remaining
	}

	// Don't print every piece download - too verbose

	data, err := peer.DownloadPiece(conn, pieceIndex, pieceLen)
	if err != nil {
		return err
	}

	expected := pieceHash(s.Torrent, pieceIndex)
	actual := sha1.Sum(data)

	if expected != actual {
		return fmt.Errorf("piece %d failed hash check", pieceIndex)
	}

	// write to file
	offset := int64(pieceIndex * s.Torrent.Info.PieceLength)
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err = s.file.WriteAt(data, offset)
	if err != nil {
		return err
	}

	fmt.Println("Piece", pieceIndex, "done")
	return nil
}
