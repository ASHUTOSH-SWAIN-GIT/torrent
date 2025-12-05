package peer

import (
	"net"

	"torrent.ashutosh.net/internal/tracker"
)

func FindWorkingPeer(peers []tracker.Peer, infoHash [20]byte, peerID [20]byte) (net.Conn, tracker.Peer, error) {

}
