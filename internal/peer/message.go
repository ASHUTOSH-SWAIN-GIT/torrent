package peer

import (
	"encoding/binary"
	"fmt"
	"io"
)

const (
	MsgChoke         = 0
	MsgUnchoke       = 1
	MsgInterested    = 2
	MsgNotInterested = 3
	MsgHave          = 4
	MsgBitfield      = 5
	MsgRequest       = 6
	MsgPiece         = 7
	MsgCancel        = 8
)

type Message struct {
	ID      byte
	Payload []byte
}

type Bitfield []byte

// whether the peer has the piece or not
func (bf Bitfield) HasPiece(index int) bool {
	byteIndex := index / 8
	bitOffset := 7 - (index % 8)

	if byteIndex < 0 || byteIndex >= len(bf) {
		return false
	}

	return (bf[byteIndex] & (1 << bitOffset)) != 0
}

func ReadMessage(r io.Reader) (*Message, error) {
	lengthBuf := make([]byte, 4)

	_, err := io.ReadFull(r, lengthBuf)
	if err != nil {
		return nil, err
	}

	length := binary.BigEndian.Uint32(lengthBuf)
	if length == 0 {
		// keepalive
		return nil, nil
	}

	id := make([]byte, 1)
	_, err = io.ReadFull(r, id)
	if err != nil {
		return nil, err
	}

	payloadLen := length - 1
	payload := make([]byte, payloadLen)

	if payloadLen > 0 {
		_, err = io.ReadFull(r, payload)
		if err != nil {
			return nil, err
		}
	}

	return &Message{
		ID:      id[0],
		Payload: payload,
	}, nil
}

// try to read a bitfield message right after handshake
func TryReadBitField(r io.Reader) (Bitfield, error) {
	msg, err := ReadMessage(r)
	if err != nil {
		return nil, err
	}

	if msg == nil {
		return nil, nil
	}

	if msg.ID == MsgBitfield {
		return Bitfield(msg.Payload), nil
	}

	return nil, nil
}

func SendIntrested(w io.Writer) error {
	buf := make([]byte, 5)
	binary.BigEndian.PutUint32(buf[0:4], 1)
	buf[4] = MsgInterested

	_, err := w.Write(buf)
	return err
}

func WaitForUnchoke(r io.Reader) error {
	for {
		msg, err := ReadMessage(r)
		if err != nil {
			return err
		}

		if msg == nil {
			continue
		}

		if msg.ID == MsgUnchoke {
			return nil
		}

		// If we get choked while waiting, return error
		if msg.ID == MsgChoke {
			return fmt.Errorf("peer choked us while waiting for unchoke")
		}
	}
}

// send messages to the peers that i have the piece
func SendHave(w io.Writer, pieceIndex int) error {
	buf := make([]byte, 9) // 4 bytes length + 1 byte ID + 4 bytes pieceindex
	binary.BigEndian.PutUint32(buf[0:4], 5)
	buf[4] = MsgHave
	binary.BigEndian.PutUint32(buf[5:9], uint32(pieceIndex))
	_, err := w.Write(buf)
	return err
}

// Sending piece message with the block data
func SendPiece(w io.Writer, index int, begin int, block []byte) error {
	length := 9 + len(block) // 1 byte ID + 4 bytes index + 4 bytes begin + block
	buf := make([]byte, 4+length)
	binary.BigEndian.PutUint32(buf[0:4], uint32(length))
	buf[4] = MsgPiece
	binary.BigEndian.PutUint32(buf[5:9], uint32(index))
	binary.BigEndian.PutUint32(buf[9:13], uint32(begin))
	copy(buf[13:], block)
	_, err := w.Write(buf)
	return err
}

// send unchoke message to the peers
func SendUnchoke(w io.Writer) error {
	buf := make([]byte, 5)
	binary.BigEndian.PutUint32(buf[0:4], 1)
	_, err := w.Write(buf)
	return err
}
