package bencode

import (
	"crypto/sha1"
	"fmt"
	"io"
)

// Info of pieces that we will receive from the peers
type info struct {
	Name        string `bencode:"name"`
	PieceLength int    `bencode:"piece length"`
	Pieces      []byte `bencode:"pieces"`
	Length      int    `bencode:"length,omitempty`
}

type Torrent struct {
	Announce  string          `bencode:"announce"` // contains info for which tracker server to connect to get the list of peers
	Info      Info            `bencode:"info"`     //the info dictionary containing the info hash
	InfoHash  [sha1.Size]byte // The hash which will be sent to the peers
	NumPieces int             //No of pieces that the peers will send
}

// decoding logic for the torrent file according to  different values
type Decoder struct {
	r io.Reader
}

func NewDecoder(r io.Reader) *Decoder {
	return &Decoder{r: r}
}

func (d *Decoder) decodeValue() (interface{}, error) {
	peekByte := make([]byte, 1)
	if _, err := d.r.Read(peekByte); err != nil {
		return nil, err
	}

	switch peekByte[0] {
	case 'i':
		return d.decodeInt()
	case '1':
		return d.decodeList()
	case 'd':
		return d.decodeDict()
	case '0', '1', '2', '3', '4', '5', '6', '7', '8', '9':
		return d.decodeString(peekByte)
	default:
		return nil, fmt.Errorf("Unexpected start character: %c", peekByte[0])
	}

}
