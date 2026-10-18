package crawler

import (
	"encoding/binary"
	"fmt"
	"math"
	"strconv"
	"sync"
	"unicode/utf8"

	"github.com/ipfs/go-cid"
)

var transcoderPool = sync.Pool{
	New: func() any {
		return &cborTranscoder{
			out: make([]byte, 0, 4096),
		}
	},
}

// cborToStrippedJSON decodes a DAG-CBOR block into JSON, converting CID link
// objects to {"$link": "..."} and stripping redundant "cid" fields (StrongRef)
// via direct CBOR token reading — no intermediate interface{} tree.
func cborToStrippedJSON(raw []byte) ([]byte, error) {
	tc := transcoderPool.Get().(*cborTranscoder)
	tc.buf = raw
	tc.pos = 0
	tc.out = tc.out[:0]

	if err := tc.transcodeValue(); err != nil {
		transcoderPool.Put(tc)
		return nil, fmt.Errorf("cbor transcode: %w", err)
	}

	result := make([]byte, len(tc.out))
	copy(result, tc.out)

	transcoderPool.Put(tc)
	return result, nil
}

// cborTranscoder reads CBOR tokens and writes JSON directly.
type cborTranscoder struct {
	buf []byte
	pos int
	out []byte
}

func (t *cborTranscoder) readByte() (byte, error) {
	if t.pos >= len(t.buf) {
		return 0, fmt.Errorf("unexpected end of CBOR data")
	}
	b := t.buf[t.pos]
	t.pos++
	return b, nil
}

func (t *cborTranscoder) readBytes(n uint64) ([]byte, error) {
	end := t.pos + int(n)
	if end > len(t.buf) || end < t.pos {
		return nil, fmt.Errorf("unexpected end of CBOR data")
	}
	b := t.buf[t.pos:end]
	t.pos = end
	return b, nil
}

func (t *cborTranscoder) readArg(ai byte) (uint64, error) {
	if ai < 24 {
		return uint64(ai), nil
	}
	switch ai {
	case 24:
		b, err := t.readByte()
		return uint64(b), err
	case 25:
		bs, err := t.readBytes(2)
		if err != nil {
			return 0, err
		}
		return uint64(binary.BigEndian.Uint16(bs)), nil
	case 26:
		bs, err := t.readBytes(4)
		if err != nil {
			return 0, err
		}
		return uint64(binary.BigEndian.Uint32(bs)), nil
	case 27:
		bs, err := t.readBytes(8)
		if err != nil {
			return 0, err
		}
		return binary.BigEndian.Uint64(bs), nil
	default:
		return 0, fmt.Errorf("unsupported CBOR additional info: %d", ai)
	}
}

func (t *cborTranscoder) transcodeValue() error {
	ib, err := t.readByte()
	if err != nil {
		return err
	}

	major := ib >> 5
	ai := ib & 0x1f

	switch major {
	case 0: // unsigned integer
		val, err := t.readArg(ai)
		if err != nil {
			return err
		}
		t.out = strconv.AppendUint(t.out, val, 10)

	case 1: // negative integer
		val, err := t.readArg(ai)
		if err != nil {
			return err
		}
		t.out = strconv.AppendInt(t.out, -1-int64(val), 10)

	case 2: // byte string
		length, err := t.readArg(ai)
		if err != nil {
			return err
		}
		if _, err = t.readBytes(length); err != nil {
			return err
		}
		t.out = append(t.out, "null"...)

	case 3: // text string
		length, err := t.readArg(ai)
		if err != nil {
			return err
		}
		bs, err := t.readBytes(length)
		if err != nil {
			return err
		}
		t.writeJSONString(bs)

	case 4: // array
		length, err := t.readArg(ai)
		if err != nil {
			return err
		}
		t.out = append(t.out, '[')
		for i := range length {
			if i > 0 {
				t.out = append(t.out, ',')
			}
			if err := t.transcodeValue(); err != nil {
				return err
			}
		}
		t.out = append(t.out, ']')

	case 5: // map
		length, err := t.readArg(ai)
		if err != nil {
			return err
		}
		t.out = append(t.out, '{')
		first := true
		for range length {
			keyBytes, err := t.readTextString()
			if err != nil {
				return err
			}

			// Strip "cid" keys (StrongRef deduplication).
			if len(keyBytes) == 3 && keyBytes[0] == 'c' && keyBytes[1] == 'i' && keyBytes[2] == 'd' {
				if err := t.skipValue(); err != nil {
					return err
				}
				continue
			}

			if !first {
				t.out = append(t.out, ',')
			}
			first = false

			t.writeJSONString(keyBytes)
			t.out = append(t.out, ':')
			if err := t.transcodeValue(); err != nil {
				return err
			}
		}
		t.out = append(t.out, '}')

	case 6: // tag
		tag, err := t.readArg(ai)
		if err != nil {
			return err
		}
		if tag == 42 {
			return t.transcodeCIDLink()
		}
		// Unknown tags: transcode the inner value.
		return t.transcodeValue()

	case 7: // simple values and floats
		switch ai {
		case 20:
			t.out = append(t.out, "false"...)
		case 21:
			t.out = append(t.out, "true"...)
		case 22:
			t.out = append(t.out, "null"...)
		case 25: // float16
			bs, err := t.readBytes(2)
			if err != nil {
				return err
			}
			f := halfToFloat64(binary.BigEndian.Uint16(bs))
			t.appendFloat(f)
		case 26: // float32
			bs, err := t.readBytes(4)
			if err != nil {
				return err
			}
			f := float64(math.Float32frombits(binary.BigEndian.Uint32(bs)))
			t.appendFloat(f)
		case 27: // float64
			bs, err := t.readBytes(8)
			if err != nil {
				return err
			}
			f := math.Float64frombits(binary.BigEndian.Uint64(bs))
			t.appendFloat(f)
		default:
			if ai < 24 {
				t.out = append(t.out, "null"...)
			} else {
				return fmt.Errorf("unsupported CBOR simple value: %d", ai)
			}
		}

	default:
		return fmt.Errorf("unsupported CBOR major type: %d", major)
	}

	return nil
}

func (t *cborTranscoder) readTextString() ([]byte, error) {
	ib, err := t.readByte()
	if err != nil {
		return nil, err
	}
	major := ib >> 5
	ai := ib & 0x1f
	if major != 3 {
		return nil, fmt.Errorf("expected text string map key, got major type %d", major)
	}
	length, err := t.readArg(ai)
	if err != nil {
		return nil, err
	}
	return t.readBytes(length)
}

// transcodeCIDLink handles DAG-CBOR tag 42 (CID link).
func (t *cborTranscoder) transcodeCIDLink() error {
	ib, err := t.readByte()
	if err != nil {
		return err
	}
	major := ib >> 5
	ai := ib & 0x1f

	if major != 2 {
		return fmt.Errorf("expected byte string in CID tag, got major type %d", major)
	}

	length, err := t.readArg(ai)
	if err != nil {
		return err
	}
	bs, err := t.readBytes(length)
	if err != nil {
		return err
	}

	// DAG-CBOR CID bytes have a 0x00 multicodec identity prefix.
	cidBytes := bs
	if len(cidBytes) > 0 && cidBytes[0] == 0x00 {
		cidBytes = cidBytes[1:]
	}

	_, c, err := cid.CidFromBytes(cidBytes)
	if err != nil {
		return fmt.Errorf("parsing CID from tag 42: %w", err)
	}

	t.out = append(t.out, `{"$link":"`...)
	t.out = append(t.out, c.String()...)
	t.out = append(t.out, `"}`...)
	return nil
}

func (t *cborTranscoder) skipValue() error {
	ib, err := t.readByte()
	if err != nil {
		return err
	}

	major := ib >> 5
	ai := ib & 0x1f

	switch major {
	case 0, 1: // integer
		_, err = t.readArg(ai)
		return err
	case 2, 3: // byte/text string
		length, err := t.readArg(ai)
		if err != nil {
			return err
		}
		_, err = t.readBytes(length)
		return err
	case 4: // array
		length, err := t.readArg(ai)
		if err != nil {
			return err
		}
		for range length {
			if err := t.skipValue(); err != nil {
				return err
			}
		}
		return nil
	case 5: // map
		length, err := t.readArg(ai)
		if err != nil {
			return err
		}
		for range length {
			if err := t.skipValue(); err != nil {
				return err
			}
			if err := t.skipValue(); err != nil {
				return err
			}
		}
		return nil
	case 6: // tag
		if _, err := t.readArg(ai); err != nil {
			return err
		}
		return t.skipValue()
	case 7: // simple values and floats
		switch {
		case ai < 24:
			return nil
		case ai == 24:
			_, err = t.readByte()
			return err
		case ai == 25:
			_, err = t.readBytes(2)
			return err
		case ai == 26:
			_, err = t.readBytes(4)
			return err
		case ai == 27:
			_, err = t.readBytes(8)
			return err
		default:
			return fmt.Errorf("unsupported CBOR ai in skip: %d", ai)
		}
	}
	return fmt.Errorf("unsupported CBOR major type in skip: %d", major)
}

func (t *cborTranscoder) writeJSONString(s []byte) {
	t.out = append(t.out, '"')
	for i := 0; i < len(s); {
		b := s[i]
		switch {
		case b == '"':
			t.out = append(t.out, '\\', '"')
			i++
		case b == '\\':
			t.out = append(t.out, '\\', '\\')
			i++
		case b < 0x20:
			switch b {
			case '\n':
				t.out = append(t.out, '\\', 'n')
			case '\r':
				t.out = append(t.out, '\\', 'r')
			case '\t':
				t.out = append(t.out, '\\', 't')
			case '\b':
				t.out = append(t.out, '\\', 'b')
			case '\f':
				t.out = append(t.out, '\\', 'f')
			default:
				t.out = append(t.out, '\\', 'u', '0', '0',
					hexDigit(b>>4), hexDigit(b&0xf))
			}
			i++
		case b < utf8.RuneSelf:
			// Fast path: scan ahead for runs of plain ASCII.
			j := i + 1
			for j < len(s) && s[j] >= 0x20 && s[j] != '"' && s[j] != '\\' && s[j] < utf8.RuneSelf {
				j++
			}
			t.out = append(t.out, s[i:j]...)
			i = j
		default:
			// Multi-byte UTF-8: pass through valid sequences.
			r, size := utf8.DecodeRune(s[i:])
			if r == utf8.RuneError && size == 1 {
				t.out = append(t.out, '\\', 'u', 'f', 'f', 'f', 'd')
				i++
			} else {
				t.out = append(t.out, s[i:i+size]...)
				i += size
			}
		}
	}
	t.out = append(t.out, '"')
}

// appendFloat writes a float as JSON, mapping NaN and Infinity to null
// since JSON has no representation for these IEEE 754 special values
// (and DAG-CBOR forbids them).
func (t *cborTranscoder) appendFloat(f float64) {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		t.out = append(t.out, "null"...)
		return
	}
	t.out = strconv.AppendFloat(t.out, f, 'g', -1, 64)
}

func hexDigit(b byte) byte {
	if b < 10 {
		return '0' + b
	}
	return 'a' + b - 10
}

func halfToFloat64(u uint16) float64 {
	sign := float64(1)
	if u&0x8000 != 0 {
		sign = -1
	}
	exp := int((u >> 10) & 0x1f)
	mant := u & 0x03ff

	switch exp {
	case 0:
		if mant == 0 {
			return sign * 0
		}
		return sign * math.Ldexp(float64(mant), -24)
	case 31:
		if mant == 0 {
			return sign * math.Inf(1)
		}
		return math.NaN()
	default:
		return sign * math.Ldexp(float64(mant|0x0400), exp-25)
	}
}
