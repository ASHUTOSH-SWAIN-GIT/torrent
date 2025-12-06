package downloader

import (
	"crypto/sha1"
	"fmt"
	"net"
	"os"
	"sync"

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
	}

	return s, nil
}

func (s *Session) Close() error {
	if s.file != nil {
		return s.file.Close()
	}
	return nil
}

// connect to the peers and download the whole file
func (s *Session) DownloadAll(peers []tracker.Peer) error {
	defer s.Close()

	numPieces := s.Torrent.NumPieces
	if numPieces == 0 {
		return fmt.Errorf("torrent has 0 pieces")
	}

	fmt.Println("Total pieces:", numPieces)

	// connect with peers and keep only succesful ones
	var conns []net.Conn

	maxPeers := 10
	for _, p := range peers {
		if len(conns) >= maxPeers {
			break
		}

		fmt.Printf("Connecting to peer %s:%d ... ", p.IP, p.Port)

		conn, err := peer.ConnectAndHandshake(p, s.Torrent.InfoHash, s.PeerID)
		if err != nil {
			fmt.Println("fail", err)
			continue
		}
		fmt.Println("ok")

		conns = append(conns, conn)
	}

	if len(conns) == 0 {
		return fmt.Errorf("could not connect to any peers")
	}

	fmt.Println("Connected peers:", len(conns))

	// create piece queue
	pieceCh := make(chan int, numPieces)
	for i := 0; i < numPieces; i++ {
		pieceCh <- i
	}
	close(pieceCh)

	var wg sync.WaitGroup
	errCh := make(chan error, len(conns))

	// worker for each connection
	for _, c := range conns {
		wg.Add(1)
		go func(conn net.Conn) {
			defer wg.Done()
			defer conn.Close()

			for pieceIndex := range pieceCh {
				if err := s.downloadoOnePieceFromPeer(conn, pieceIndex); err != nil {
					fmt.Println("Error downloading piece", pieceIndex, "from peer", err)
					errCh <- err
					return
				}
			}
		}(c)
	}

	wg.Wait()

	// after all workers are done
	select {
	case err := <-errCh:
		return err
	default:
		return nil
	}
}

// download  a single  piece from the peer over one connection
func (s *Session) downloadoOnePieceFromPeer(conn net.Conn, pieceIndex int) error {
	pieceLen := s.Torrent.Info.PieceLength

	if pieceIndex == s.Torrent.NumPieces-1 {
		totalLen := s.Torrent.Info.Length
		remaining := totalLen - pieceIndex*s.Torrent.Info.PieceLength
		pieceLen = remaining
	}

	fmt.Println("Peer ", conn.RemoteAddr(), "is downloading piece", pieceIndex)

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
