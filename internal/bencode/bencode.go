package bencode

import (
	"bytes"
	"crypto/sha1"
	"fmt"
	"io"
	"strconv"
)

// Info of pieces that we will receive from the peers
type Info struct {
	Name        string `bencode:"name"`
	PieceLength int    `bencode:"piece length"`
	Pieces      []byte `bencode:"pieces"`
	Length      int    `bencode:"length,omitempty"`
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
	case 'l':
		return d.decodeList()
	case 'd':
		return d.decodeDict()
	case '0', '1', '2', '3', '4', '5', '6', '7', '8', '9':
		return d.decodeString(peekByte)
	default:
		return nil, fmt.Errorf("Unexpected start character: %c", peekByte[0])
	}

}

func (d *Decoder) decodeInt() (int64, error) {
	var buf bytes.Buffer
	for {
		b := make([]byte, 1)
		if _, err := d.r.Read(b); err != nil {
			return 0, err
		}
		if b[0] == 'e' {
			break
		}
		buf.WriteByte(b[0])
	}

	str := buf.String()
	if len(str) == 0 {
		return 0, fmt.Errorf("Empty integer value")
	}
	if len(str) > 1 && str[0] == '0' {
		return 0, fmt.Errorf("integer with leading 0: %s", str)
	}

	return strconv.ParseInt(str, 10, 64)
}

func (d *Decoder) decodeString(firstByte []byte) ([]byte, error) {
	var lenBuf bytes.Buffer
	lenBuf.Write(firstByte)
	for {
		b := make([]byte, 1)
		if _, err := d.r.Read(b); err != nil {
			return nil, err
		}
		if b[0] == ':' {
			break
		}
		lenBuf.WriteByte(b[0])
	}
	length, err := strconv.Atoi(lenBuf.String())
	if err != nil {
		return nil, fmt.Errorf("Invalid string length prefix: %w", err)
	}

	str := make([]byte, length)
	if _, err := io.ReadFull(d.r, str); err != nil {
		return nil, fmt.Errorf("Unexpected EOF reading string content")
	}
	return str, nil
}

func (d *Decoder) decodeList() ([]interface{}, error) {
	var list []interface{}
	for {
		peekByte := make([]byte, 1)
		if _, err := d.r.Read(peekByte); err != nil {
			return nil, err
		}

		if peekByte[0] == 'e' {
			break
		}
		d.r = io.MultiReader(bytes.NewReader(peekByte), d.r)

		val, err := d.decodeValue()
		if err != nil {
			return nil, err
		}
		list = append(list, val)
	}

	return list, nil
}

func (d *Decoder) decodeDict() (map[string]interface{}, error) {
	dict := make(map[string]interface{})

	for {
		peekByte := make([]byte, 1)
		if _, err := d.r.Read(peekByte); err != nil {
			return nil, err
		}

		if peekByte[0] == 'e' {
			break
		}

		d.r = io.MultiReader(bytes.NewReader(peekByte), d.r)

		KeyBytes, err := d.decodeValue()
		if err != nil {
			return nil, err
		}

		key, ok := KeyBytes.([]byte)

		if !ok {
			return nil, fmt.Errorf("dictionary key must be a byte string")
		}

		val, err := d.decodeValue()
		if err != nil {
			return nil, err
		}
		dict[string(key)] = val

	}
	return dict, nil
}

func (d *Decoder) readByte() (byte, error) {
	p := make([]byte, 1)
	if _, err := io.ReadFull(d.r, p); err != nil {
		return 0, err
	}
	return p[0], nil
}

func Unmarshal(data []byte) (map[string]interface{}, error) {
	d := NewDecoder(bytes.NewReader(data))
	return d.decodeDict()
}
