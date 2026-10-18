package repoarchive

import "time"

// File format magic bytes and version.
var Magic = [8]byte{'R', 'E', 'P', 'O', 'A', 'R', 'C', 'H'}

const (
	Version = uint16(1)

	FileHeaderSize = 64
	FileFooterSize = 32

	DefaultSegmentSize = 2 * 1024 * 1024 * 1024 // 2GB

	DefaultZstdLevel = 3
)

type FileHeader struct {
	Magic       [8]byte  // "REPOARCH"
	Version     uint16   // 1
	Flags       uint16   // reserved
	CreatedAt   int64    // unix microseconds
	RepoCount   uint32   // set at finalize
	IndexOffset int64    // set at finalize
	Reserved    [32]byte // zero-filled, pads header to exactly 64 bytes
}

type FileFooter struct {
	IndexOffset int64    // byte offset of index section
	IndexSize   uint32   // byte size of index section
	RepoCount   uint32   // total repos in this segment
	CRC32       uint32   // CRC32 of the index section
	Reserved    [4]byte  // pads footer to exactly 32 bytes
	Magic       [8]byte  // "REPOARCH"
}

type IndexEntry struct {
	DID    string
	Offset int64  // byte offset of the repo block in the segment
	Size   uint32 // byte size of the repo block
	CRC32  uint32 // CRC32 of the repo block
}

type RepoHeader struct {
	TotalBlockSize  uint32 // enables skipping entire repo
	DID             string
	PDS             string
	Rev             string // repo revision for future incremental
	CrawledAt       time.Time
	CollectionCount uint16
	CRC32           uint32 // CRC32 of everything after the header
}

type CollectionTOCEntry struct {
	Name           string
	RecordCount    uint32
	CompressedSize uint32 // byte size of the zstd-compressed data block
}

type Record struct {
	RKey string
	JSON []byte // CID-stripped JSON
}

// SerializedRepo is a pre-compressed repo block ready for sequential file writing.
// Workers produce these by calling SerializeRepoBlock, and the writer consumes them
// via WriteSerializedRepo — keeping compression parallel and I/O sequential.
type SerializedRepo struct {
	DID       string
	BlockData []byte
	BlockCRC  uint32
}

type CrawledRepo struct {
	DID         string
	PDS         string
	Rev         string
	CrawledAt   time.Time
	Collections map[string][]Record // collection name -> records
}
