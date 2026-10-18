package repoarchive

import (
	"bytes"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/klauspost/compress/zstd"
)

// SegmentWriter writes CrawledRepo data into .rca segment files.
// It receives repos via a channel and writes them sequentially.
// When the current segment exceeds the target size, it finalizes
// the segment (writing index + footer) and opens a new one.
type SegmentWriter struct {
	outputDir   string
	segmentSize int64
	zstdLevel   int

	mu            sync.Mutex
	segmentNum    int
	file          *os.File
	offset        int64
	repoCount     uint32
	indexEntries  []IndexEntry
	createdAt     time.Time
	bytesWritten  int64
	totalRepos    uint32
}

type WriterOption func(*SegmentWriter)

func WithSegmentSize(size int64) WriterOption {
	return func(w *SegmentWriter) { w.segmentSize = size }
}

func WithZstdLevel(level int) WriterOption {
	return func(w *SegmentWriter) { w.zstdLevel = level }
}

func WithStartSegment(n int) WriterOption {
	return func(w *SegmentWriter) { w.segmentNum = n }
}

func NewSegmentWriter(outputDir string, opts ...WriterOption) (*SegmentWriter, error) {
	w := &SegmentWriter{
		outputDir:   outputDir,
		segmentSize: DefaultSegmentSize,
		zstdLevel:   DefaultZstdLevel,
		segmentNum:  1,
	}
	for _, opt := range opts {
		opt(w)
	}

	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return nil, fmt.Errorf("creating output dir: %w", err)
	}

	if err := w.openSegment(); err != nil {
		return nil, err
	}

	return w, nil
}

func (w *SegmentWriter) SegmentNum() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.segmentNum
}

func (w *SegmentWriter) TotalRepos() uint32 {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.totalRepos
}

func (w *SegmentWriter) BytesWritten() int64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.bytesWritten
}

func (w *SegmentWriter) segmentPath(num int) string {
	return filepath.Join(w.outputDir, fmt.Sprintf("segment_%04d.rca", num))
}

func (w *SegmentWriter) openSegment() error {
	path := w.segmentPath(w.segmentNum)
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("creating segment file %s: %w", path, err)
	}

	w.file = f
	w.offset = 0
	w.repoCount = 0
	w.indexEntries = w.indexEntries[:0]
	w.createdAt = time.Now()

	header := &FileHeader{
		Magic:     Magic,
		Version:   Version,
		CreatedAt: w.createdAt.UnixMicro(),
	}
	if err := writeFileHeader(f, header); err != nil {
		return fmt.Errorf("writing file header: %w", err)
	}
	w.offset = FileHeaderSize

	return nil
}

func (w *SegmentWriter) WriteRepo(repo *CrawledRepo) error {
	enc, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.EncoderLevel(w.zstdLevel)))
	if err != nil {
		return fmt.Errorf("creating zstd encoder: %w", err)
	}
	defer enc.Close()

	sr, err := SerializeRepoBlock(enc, repo)
	if err != nil {
		return err
	}
	return w.WriteSerializedRepo(sr)
}

// WriteSerializedRepo writes a pre-serialized repo block to the current segment.
// This is the fast path: only sequential file I/O and index bookkeeping, no compression.
func (w *SegmentWriter) WriteSerializedRepo(sr *SerializedRepo) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	repoOffset := w.offset

	if _, err := w.file.Write(sr.BlockData); err != nil {
		return fmt.Errorf("writing repo block: %w", err)
	}

	w.offset += int64(len(sr.BlockData))
	w.repoCount++
	w.totalRepos++
	w.bytesWritten += int64(len(sr.BlockData))

	w.indexEntries = append(w.indexEntries, IndexEntry{
		DID:    sr.DID,
		Offset: repoOffset,
		Size:   uint32(len(sr.BlockData)),
		CRC32:  sr.BlockCRC,
	})

	// Check if we need to rotate segments.
	if w.offset >= w.segmentSize {
		if err := w.finalizeSegment(); err != nil {
			return fmt.Errorf("finalizing segment: %w", err)
		}
		w.segmentNum++
		if err := w.openSegment(); err != nil {
			return fmt.Errorf("opening new segment: %w", err)
		}
	}

	return nil
}

// SerializeRepoBlock builds the binary repo block for a single CrawledRepo.
// The encoder is caller-owned and can be reused across calls (e.g. via sync.Pool).
// Returns a SerializedRepo ready for WriteSerializedRepo.
func SerializeRepoBlock(enc *zstd.Encoder, repo *CrawledRepo) (*SerializedRepo, error) {
	collNames := make([]string, 0, len(repo.Collections))
	for name := range repo.Collections {
		collNames = append(collNames, name)
	}
	sort.Strings(collNames)

	type collBlock struct {
		name       string
		records    []Record
		compressed []byte
	}
	collBlocks := make([]collBlock, 0, len(collNames))
	for _, name := range collNames {
		records := repo.Collections[name]
		if len(records) == 0 {
			continue
		}

		var raw bytes.Buffer
		for _, rec := range records {
			if err := writeLenPrefixed(&raw, []byte(rec.RKey)); err != nil {
				return nil, err
			}
			if err := writeLenPrefixed32(&raw, rec.JSON); err != nil {
				return nil, err
			}
		}

		compressed := enc.EncodeAll(raw.Bytes(), nil)
		collBlocks = append(collBlocks, collBlock{
			name:       name,
			records:    records,
			compressed: compressed,
		})
	}

	// Build the complete repo block into a buffer.
	// We write everything after total_block_size first, then prepend the size.
	var inner bytes.Buffer

	if err := writeLenPrefixed(&inner, []byte(repo.DID)); err != nil {
		return nil, err
	}
	if err := writeLenPrefixed(&inner, []byte(repo.PDS)); err != nil {
		return nil, err
	}
	if err := writeLenPrefixed(&inner, []byte(repo.Rev)); err != nil {
		return nil, err
	}

	if err := writeInt64(&inner, repo.CrawledAt.UnixMicro()); err != nil {
		return nil, err
	}

	if err := writeUint16(&inner, uint16(len(collBlocks))); err != nil {
		return nil, err
	}

	// CRC32 placeholder — we'll fill this after computing.
	crcOffset := inner.Len()
	if err := writeUint32(&inner, 0); err != nil {
		return nil, err
	}

	for _, cb := range collBlocks {
		if err := writeLenPrefixed(&inner, []byte(cb.name)); err != nil {
			return nil, err
		}
		if err := writeUint32(&inner, uint32(len(cb.records))); err != nil {
			return nil, err
		}
		if err := writeUint32(&inner, uint32(len(cb.compressed))); err != nil {
			return nil, err
		}
	}

	for _, cb := range collBlocks {
		if _, err := inner.Write(cb.compressed); err != nil {
			return nil, err
		}
	}

	innerBytes := inner.Bytes()
	afterCRC := innerBytes[crcOffset+4:]
	crcVal := checksumCRC32(afterCRC)

	innerBytes[crcOffset] = byte(crcVal)
	innerBytes[crcOffset+1] = byte(crcVal >> 8)
	innerBytes[crcOffset+2] = byte(crcVal >> 16)
	innerBytes[crcOffset+3] = byte(crcVal >> 24)

	// Prepend total_block_size (4 bytes + inner).
	totalSize := uint32(4 + len(innerBytes))
	var block bytes.Buffer
	block.Grow(int(totalSize))
	if err := writeUint32(&block, totalSize); err != nil {
		return nil, err
	}
	block.Write(innerBytes)

	blockBytes := block.Bytes()
	blockCRC := crc32.ChecksumIEEE(blockBytes)
	return &SerializedRepo{
		DID:       repo.DID,
		BlockData: blockBytes,
		BlockCRC:  blockCRC,
	}, nil
}

func (w *SegmentWriter) finalizeSegment() error {
	indexOffset := w.offset

	sort.Slice(w.indexEntries, func(i, j int) bool {
		return w.indexEntries[i].DID < w.indexEntries[j].DID
	})

	var indexBuf bytes.Buffer
	if err := writeUint32(&indexBuf, uint32(len(w.indexEntries))); err != nil {
		return err
	}
	for _, entry := range w.indexEntries {
		if err := writeLenPrefixed(&indexBuf, []byte(entry.DID)); err != nil {
			return err
		}
		if err := writeInt64(&indexBuf, entry.Offset); err != nil {
			return err
		}
		if err := writeUint32(&indexBuf, entry.Size); err != nil {
			return err
		}
		if err := writeUint32(&indexBuf, entry.CRC32); err != nil {
			return err
		}
	}

	indexData := indexBuf.Bytes()
	indexCRC := checksumCRC32(indexData)

	if _, err := w.file.Write(indexData); err != nil {
		return fmt.Errorf("writing index section: %w", err)
	}
	w.offset += int64(len(indexData))

	footer := &FileFooter{
		IndexOffset: indexOffset,
		IndexSize:   uint32(len(indexData)),
		RepoCount:   w.repoCount,
		CRC32:       indexCRC,
		Magic:       Magic,
	}
	if err := writeFileFooter(w.file, footer); err != nil {
		return fmt.Errorf("writing footer: %w", err)
	}

	if _, err := w.file.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("seeking to header: %w", err)
	}
	header := &FileHeader{
		Magic:       Magic,
		Version:     Version,
		CreatedAt:   w.createdAt.UnixMicro(),
		RepoCount:   w.repoCount,
		IndexOffset: indexOffset,
	}
	if err := writeFileHeader(w.file, header); err != nil {
		return fmt.Errorf("updating file header: %w", err)
	}

	return w.file.Close()
}

func (w *SegmentWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.file == nil {
		return nil
	}

	if err := w.finalizeSegment(); err != nil {
		return err
	}

	w.file = nil
	return nil
}
