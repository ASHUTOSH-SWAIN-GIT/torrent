package peer

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
)

const DefaultBlockSize = 16 * 1024

func SendRequest(w io.Writer, index int, begin int, length int) error {
	buf := make([]byte, 17)
	binary.BigEndian.PutUint32(buf[0:4], 13)
	buf[4] = MsgRequest
	binary.BigEndian.PutUint32(buf[5:9], uint32(index))
	binary.BigEndian.PutUint32(buf[9:13], uint32(begin))
	binary.BigEndian.PutUint32(buf[13:17], uint32(length))

	_, err := w.Write(buf)
	return err
}

func ReadPiece(r io.Reader) (index int, begin int, block []byte, err error) {
	msg, err := ReadMessage(r)
	if err != nil {
		return 0, 0, nil, err
	}

	if msg == nil {
		return 0, 0, nil, fmt.Errorf("received keepalive during expected piece")
	}

	if msg.ID != MsgPiece {
		return 0, 0, nil, fmt.Errorf("expected piece message, got %d", msg.ID)
	}

	if len(msg.Payload) < 8 {
		return 0, 0, nil, fmt.Errorf("piece payload too small")
	}

	index = int(binary.BigEndian.Uint32(msg.Payload[0:4]))
	begin = int(binary.BigEndian.Uint32(msg.Payload[4:8]))
	block = msg.Payload[8:]

	return index, begin, block, nil
}

// sends  request for a whole block and reads the matching peice
func DownloadBlock(conn net.Conn, index int, begin int, length int) ([]byte, error) {

	// send intrested message to the peer
	if err := SendIntrested(conn); err != nil {
		return nil, err
	}

	// wait for unchoke mess from the peer
	if err := WaitForUnchoke(conn); err != nil {
		return nil, err
	}

	// send request for the block
	if err := SendRequest(conn, index, begin, length); err != nil {
		return nil, err
	}

	_, blkBegin, blkData, err := ReadPiece(conn)
	if err != nil {
		return nil, err
	}

	if blkBegin != begin {
		return nil, fmt.Errorf("unexpected block offset got  %d want %d", blkBegin, begin)
	}

	if len(blkData) != length {
		return nil, fmt.Errorf("unexpected block length got %d want %d", len(blkData))
	}

	return blkData, nil

}

// downloads a torrent peice by requesting  blocks
func DownloadPiece(conn net.Conn, pieceIndex int, pieceLength int) ([]byte, error) {
	buf := make([]byte, pieceLength)
	offset := 0

	for offset < pieceLength {
		blockSize := DefaultBlockSize
		if offset+blockSize > pieceLength {
			blockSize = pieceLength - offset
		}

		block, err := DownloadBlock(conn, pieceIndex, offset, blockSize)
		if err != nil {
			return nil, err
		}

		copy(buf[offset:], block)
		offset += blockSize
	}

	return buf, nil
}
