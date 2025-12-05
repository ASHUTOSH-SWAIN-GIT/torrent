package peer

import (
	"context"
	"fmt"
	"net"

	"torrent.ashutosh.net/internal/tracker"
)

func FindWorkingPeer(peers []tracker.Peer, infoHash [20]byte, peerID [20]byte) (net.Conn, tracker.Peer, error) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	results := make(chan struct {
		conn net.Conn
		peer tracker.Peer
		err  error
	})

	// no of peers to test in parallel
	parallel := 40
	if len(peers) < parallel {
		parallel = len(peers)
	}

	for i := 0; i < parallel; i++ {
		p := peers[i]

		go func(peer tracker.Peer) {

			select {
			case <-ctx.Done():
				return
			default:
			}

			conn, err := ConnectAndHandshake(peer, infoHash, peerID)
			if err == nil {
				results <- struct {
					conn net.Conn
					peer tracker.Peer
					err  error
				}{conn, peer, nil}
				return
			}

			results <- struct {
				conn net.Conn
				peer tracker.Peer
				err  error
			}{nil, peer, err}
		}(p)
	}

	for i := 0; i < parallel; i++ {
		result := <-results

		if result.err == nil {
			return result.conn, result.peer, nil
		}
	}

	return nil, tracker.Peer{}, fmt.Errorf("no working peer found")

}
