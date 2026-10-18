package repoarchive

import (
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"io"
)

func writeUint16(w io.Writer, v uint16) error {
	var buf [2]byte
	binary.LittleEndian.PutUint16(buf[:], v)
	_, err := w.Write(buf[:])
	return err
}

func writeUint32(w io.Writer, v uint32) error {
	var buf [4]byte
	binary.LittleEndian.PutUint32(buf[:], v)
	_, err := w.Write(buf[:])
	return err
}

func writeInt64(w io.Writer, v int64) error {
	var buf [8]byte
	binary.LittleEndian.PutUint64(buf[:], uint64(v))
	_, err := w.Write(buf[:])
	return err
}

func writeLenPrefixed(w io.Writer, data []byte) error {
	if len(data) > 65535 {
		return fmt.Errorf("data too long for uint16 length prefix: %d", len(data))
	}
	if err := writeUint16(w, uint16(len(data))); err != nil {
		return err
	}
	_, err := w.Write(data)
	return err
}

func writeLenPrefixed32(w io.Writer, data []byte) error {
	if err := writeUint32(w, uint32(len(data))); err != nil {
		return err
	}
	_, err := w.Write(data)
	return err
}

func readUint16(r io.Reader) (uint16, error) {
	var buf [2]byte
	if _, err := io.ReadFull(r, buf[:]); err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint16(buf[:]), nil
}

func readUint32(r io.Reader) (uint32, error) {
	var buf [4]byte
	if _, err := io.ReadFull(r, buf[:]); err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint32(buf[:]), nil
}

func readInt64(r io.Reader) (int64, error) {
	var buf [8]byte
	if _, err := io.ReadFull(r, buf[:]); err != nil {
		return 0, err
	}
	return int64(binary.LittleEndian.Uint64(buf[:])), nil
}

func readLenPrefixed(r io.Reader) ([]byte, error) {
	length, err := readUint16(r)
	if err != nil {
		return nil, err
	}
	buf := make([]byte, length)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, err
	}
	return buf, nil
}

func readLenPrefixed32(r io.Reader) ([]byte, error) {
	length, err := readUint32(r)
	if err != nil {
		return nil, err
	}
	buf := make([]byte, length)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, err
	}
	return buf, nil
}

func writeFileHeader(w io.Writer, h *FileHeader) error {
	if _, err := w.Write(h.Magic[:]); err != nil {
		return err
	}
	if err := writeUint16(w, h.Version); err != nil {
		return err
	}
	if err := writeUint16(w, h.Flags); err != nil {
		return err
	}
	if err := writeInt64(w, h.CreatedAt); err != nil {
		return err
	}
	if err := writeUint32(w, h.RepoCount); err != nil {
		return err
	}
	if err := writeInt64(w, h.IndexOffset); err != nil {
		return err
	}
	_, err := w.Write(h.Reserved[:])
	return err
}

func readFileHeader(r io.Reader) (*FileHeader, error) {
	h := &FileHeader{}
	if _, err := io.ReadFull(r, h.Magic[:]); err != nil {
		return nil, err
	}
	if h.Magic != Magic {
		return nil, fmt.Errorf("invalid magic: got %q, want %q", h.Magic, Magic)
	}
	var err error
	h.Version, err = readUint16(r)
	if err != nil {
		return nil, err
	}
	if h.Version != Version {
		return nil, fmt.Errorf("unsupported version: %d", h.Version)
	}
	h.Flags, err = readUint16(r)
	if err != nil {
		return nil, err
	}
	h.CreatedAt, err = readInt64(r)
	if err != nil {
		return nil, err
	}
	h.RepoCount, err = readUint32(r)
	if err != nil {
		return nil, err
	}
	h.IndexOffset, err = readInt64(r)
	if err != nil {
		return nil, err
	}
	if _, err := io.ReadFull(r, h.Reserved[:]); err != nil {
		return nil, err
	}
	return h, nil
}

func writeFileFooter(w io.Writer, f *FileFooter) error {
	if err := writeInt64(w, f.IndexOffset); err != nil {
		return err
	}
	if err := writeUint32(w, f.IndexSize); err != nil {
		return err
	}
	if err := writeUint32(w, f.RepoCount); err != nil {
		return err
	}
	if err := writeUint32(w, f.CRC32); err != nil {
		return err
	}
	if _, err := w.Write(f.Reserved[:]); err != nil {
		return err
	}
	_, err := w.Write(f.Magic[:])
	return err
}

func readFileFooter(r io.Reader) (*FileFooter, error) {
	f := &FileFooter{}
	var err error
	f.IndexOffset, err = readInt64(r)
	if err != nil {
		return nil, err
	}
	f.IndexSize, err = readUint32(r)
	if err != nil {
		return nil, err
	}
	f.RepoCount, err = readUint32(r)
	if err != nil {
		return nil, err
	}
	f.CRC32, err = readUint32(r)
	if err != nil {
		return nil, err
	}
	if _, err := io.ReadFull(r, f.Reserved[:]); err != nil {
		return nil, err
	}
	if _, err := io.ReadFull(r, f.Magic[:]); err != nil {
		return nil, err
	}
	if f.Magic != Magic {
		return nil, fmt.Errorf("invalid footer magic: got %q, want %q", f.Magic, Magic)
	}
	return f, nil
}

func checksumCRC32(data []byte) uint32 {
	return crc32.ChecksumIEEE(data)
}
