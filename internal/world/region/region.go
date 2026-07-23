// SPDX-License-Identifier: AGPL-3.0-only

// Package region safely reads and replaces Anvil region-file chunk records.
package region

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"
)

const (
	SectorBytes    = 4096
	HeaderBytes    = SectorBytes * 2
	ChunkCount     = 1024
	MaxChunkBytes  = 16 << 20
	MaxRegionBytes = 512 << 20
)

var ErrChunkMissing = errors.New("chunk is not present in region")

type Location struct {
	SectorOffset uint32
	SectorCount  uint8
	Timestamp    uint32
}
type Header [ChunkCount]Location

func Index(localX, localZ int) (int, error) {
	if localX < 0 || localX >= 32 || localZ < 0 || localZ >= 32 {
		return 0, fmt.Errorf("local chunk coordinates (%d,%d) outside 0..31", localX, localZ)
	}
	return localX + localZ*32, nil
}

func ParseHeader(data []byte, fileSize int64) (Header, error) {
	var h Header
	if len(data) < HeaderBytes {
		return h, fmt.Errorf("region header is %d bytes; need %d", len(data), HeaderBytes)
	}
	sectors := (fileSize + SectorBytes - 1) / SectorBytes
	used := map[uint32]int{0: -1, 1: -1}
	for i := 0; i < ChunkCount; i++ {
		raw := binary.BigEndian.Uint32(data[i*4 : i*4+4])
		off, count := raw>>8, uint8(raw)
		ts := binary.BigEndian.Uint32(data[SectorBytes+i*4 : SectorBytes+i*4+4])
		h[i] = Location{off, count, ts}
		if off == 0 && count == 0 {
			continue
		}
		if off < 2 || count == 0 || int64(off)+int64(count) > sectors {
			return h, fmt.Errorf("chunk index %d has invalid sector range offset=%d count=%d", i, off, count)
		}
		for s := off; s < off+uint32(count); s++ {
			if other, exists := used[s]; exists {
				return h, fmt.Errorf("chunk indexes %d and %d overlap at sector %d", other, i, s)
			}
			used[s] = i
		}
	}
	return h, nil
}

func ReadChunk(path string, localX, localZ int) ([]byte, error) {
	idx, err := Index(localX, localZ)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrChunkMissing
		}
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	headerBytes := make([]byte, HeaderBytes)
	if _, err := io.ReadFull(f, headerBytes); err != nil {
		return nil, err
	}
	h, err := ParseHeader(headerBytes, st.Size())
	if err != nil {
		return nil, fmt.Errorf("parse %q: %w", path, err)
	}
	loc := h[idx]
	if loc.SectorOffset == 0 {
		return nil, ErrChunkMissing
	}
	start := int64(loc.SectorOffset) * SectorBytes
	var prefix [5]byte
	if _, err := f.ReadAt(prefix[:], start); err != nil {
		return nil, err
	}
	n := int(binary.BigEndian.Uint32(prefix[:4]))
	allocated := int(loc.SectorCount) * SectorBytes
	if n < 1 || n+4 > allocated {
		return nil, fmt.Errorf("invalid chunk record length %d for %d allocated bytes", n, allocated)
	}
	if start+int64(4+n) > st.Size() {
		return nil, fmt.Errorf("chunk record length %d extends beyond region file", n)
	}
	record := make([]byte, 4+n)
	copy(record[:5], prefix[:])
	if _, err := f.ReadAt(record[5:], start+5); err != nil {
		return nil, err
	}
	return decodeRecord(record)
}

func decodeRecord(record []byte) ([]byte, error) {
	if len(record) < 5 {
		return nil, fmt.Errorf("chunk record too short")
	}
	n := int(binary.BigEndian.Uint32(record[:4]))
	if n < 1 || n > len(record)-4 {
		return nil, fmt.Errorf("invalid chunk record length %d", n)
	}
	compression := record[4]
	if compression&0x80 != 0 {
		return nil, fmt.Errorf("external chunk streams are not supported")
	}
	payload := record[5 : 4+n]
	var r io.Reader
	switch compression {
	case 1:
		zr, err := gzip.NewReader(bytes.NewReader(payload))
		if err != nil {
			return nil, err
		}
		defer zr.Close()
		r = zr
	case 2:
		zr, err := zlib.NewReader(bytes.NewReader(payload))
		if err != nil {
			return nil, err
		}
		defer zr.Close()
		r = zr
	case 3:
		r = bytes.NewReader(payload)
	default:
		return nil, fmt.Errorf("unsupported chunk compression type %d", compression)
	}
	out, err := io.ReadAll(io.LimitReader(r, MaxChunkBytes+1))
	if err != nil {
		return nil, err
	}
	if len(out) > MaxChunkBytes {
		return nil, fmt.Errorf("decompressed chunk exceeds %d bytes", MaxChunkBytes)
	}
	return out, nil
}

func EncodeZlibRecord(nbtData []byte) ([]byte, error) {
	if len(nbtData) > MaxChunkBytes {
		return nil, fmt.Errorf("chunk NBT exceeds %d bytes", MaxChunkBytes)
	}
	var compressed bytes.Buffer
	zw := zlib.NewWriter(&compressed)
	if _, err := zw.Write(nbtData); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	n := compressed.Len() + 1
	record := make([]byte, 4+n)
	binary.BigEndian.PutUint32(record[:4], uint32(n))
	record[4] = 2
	copy(record[5:], compressed.Bytes())
	return record, nil
}

func WorldToRegion(chunk int32) (region, local int32) {
	region = chunk >> 5
	local = chunk & 31
	return
}

func TimestampNow() uint32 { return uint32(time.Now().Unix()) }

func RegionPath(worldPath string, regionX, regionZ int32) string {
	return filepath.Join(worldPath, "region", fmt.Sprintf("r.%d.%d.mca", regionX, regionZ))
}

func EntityRegionPath(worldPath string, regionX, regionZ int32) string {
	return filepath.Join(worldPath, "entities", fmt.Sprintf("r.%d.%d.mca", regionX, regionZ))
}

// Store serializes rewrites of each region file. Rewrites use a temporary file
// and atomic rename, preserving every untouched compressed chunk record byte-for-byte.
type Store struct {
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

func NewStore() *Store { return &Store{locks: make(map[string]*sync.Mutex)} }

func (s *Store) WriteChunk(path string, localX, localZ int, nbtData []byte) error {
	s.mu.Lock()
	lock := s.locks[path]
	if lock == nil {
		lock = new(sync.Mutex)
		s.locks[path] = lock
	}
	s.mu.Unlock()
	lock.Lock()
	defer lock.Unlock()
	return rewriteChunk(path, localX, localZ, nbtData)
}

func rewriteChunk(path string, localX, localZ int, nbtData []byte) error {
	idx, err := Index(localX, localZ)
	if err != nil {
		return err
	}
	st, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("refusing to create missing region file %q", path)
		}
		return err
	}
	if st.Size() < HeaderBytes || st.Size() > MaxRegionBytes {
		return fmt.Errorf("region file size %d outside safe range", st.Size())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	header, err := ParseHeader(data[:HeaderBytes], int64(len(data)))
	if err != nil {
		return err
	}
	if header[idx].SectorOffset == 0 {
		return fmt.Errorf("%w: refusing to create synthetic chunk", ErrChunkMissing)
	}
	records := make([][]byte, ChunkCount)
	for i, loc := range header {
		if loc.SectorOffset == 0 {
			continue
		}
		start := int(loc.SectorOffset) * SectorBytes
		allocated := int(loc.SectorCount) * SectorBytes
		if start+5 > len(data) || allocated < 5 {
			return fmt.Errorf("chunk index %d record header outside region", i)
		}
		n := int(binary.BigEndian.Uint32(data[start : start+4]))
		if n < 1 || n+4 > allocated || start+4+n > len(data) {
			return fmt.Errorf("chunk index %d has invalid record length %d", i, n)
		}
		records[i] = append([]byte(nil), data[start:start+4+n]...)
	}
	records[idx], err = EncodeZlibRecord(nbtData)
	if err != nil {
		return err
	}
	header[idx].Timestamp = TimestampNow()
	out := make([]byte, HeaderBytes)
	sector := uint32(2)
	for i, record := range records {
		if record == nil {
			continue
		}
		count := (len(record) + SectorBytes - 1) / SectorBytes
		if count > 255 || sector > 0xffffff {
			return fmt.Errorf("region allocation exceeds Anvil header limits")
		}
		binary.BigEndian.PutUint32(out[i*4:i*4+4], sector<<8|uint32(count))
		binary.BigEndian.PutUint32(out[SectorBytes+i*4:SectorBytes+i*4+4], header[i].Timestamp)
		out = append(out, record...)
		out = append(out, make([]byte, count*SectorBytes-len(record))...)
		sector += uint32(count)
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".golem-region-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	ok := false
	defer func() {
		_ = tmp.Close()
		if !ok {
			_ = os.Remove(tmpName)
		}
	}()
	if err := tmp.Chmod(st.Mode().Perm()); err != nil {
		return err
	}
	if _, err := tmp.Write(out); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	if runtime.GOOS != "windows" {
		d, err := os.Open(dir)
		if err != nil {
			return err
		}
		if err := d.Sync(); err != nil {
			_ = d.Close()
			return err
		}
		if err := d.Close(); err != nil {
			return err
		}
	}
	ok = true
	return nil
}
