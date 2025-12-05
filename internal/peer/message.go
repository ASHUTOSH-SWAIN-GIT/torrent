package peer

import (
	"encoding/binary"
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

func ReadMessage(r io.Reader) (*Message, error) {
	lengthBuf := make([]byte, 4)

	_, err := io.ReadFull(r, lengthBuf)
	if err != nil {
		return nil, err
	}

	length := binary.BigEndian.Uint32(lengthBuf)
	if length == 0 {
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
	}
}
