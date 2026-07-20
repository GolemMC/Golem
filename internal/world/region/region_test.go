// SPDX-License-Identifier: AGPL-3.0-only

package region

import (
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestIndex(t *testing.T) {
	got, err := Index(31, 31)
	if err != nil || got != 1023 {
		t.Fatalf("got %d, %v", got, err)
	}
	if _, err := Index(32, 0); err == nil {
		t.Fatal("expected bounds error")
	}
}

func TestHeaderRejectsOverlap(t *testing.T) {
	b := make([]byte, HeaderBytes)
	binary.BigEndian.PutUint32(b[0:4], 2<<8|1)
	binary.BigEndian.PutUint32(b[4:8], 2<<8|1)
	if _, err := ParseHeader(b, 3*SectorBytes); err == nil {
		t.Fatal("expected overlap error")
	}
}

func TestReadMissingRegion(t *testing.T) {
	_, err := ReadChunk(filepath.Join(t.TempDir(), "missing.mca"), 0, 0)
	if !errors.Is(err, ErrChunkMissing) {
		t.Fatalf("got %v", err)
	}
}

func FuzzRegionHeader(f *testing.F) {
	f.Add(make([]byte, HeaderBytes), int64(HeaderBytes))
	f.Fuzz(func(t *testing.T, b []byte, size int64) {
		if size < 0 {
			size = 0
		}
		_, _ = ParseHeader(b, size)
	})
}

func TestReadZlibChunk(t *testing.T) {
	record, err := EncodeZlibRecord([]byte{10, 0, 0, 0})
	if err != nil {
		t.Fatal(err)
	}
	sectors := (len(record) + SectorBytes - 1) / SectorBytes
	b := make([]byte, HeaderBytes+sectors*SectorBytes)
	binary.BigEndian.PutUint32(b[:4], 2<<8|uint32(sectors))
	copy(b[HeaderBytes:], record)
	path := filepath.Join(t.TempDir(), "r.0.0.mca")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := ReadChunk(path, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string([]byte{10, 0, 0, 0}) {
		t.Fatalf("got %v", got)
	}
}

func TestReadChunkAllowsUnpaddedFinalSector(t *testing.T) {
	record, err := EncodeZlibRecord([]byte{10, 0, 0, 0})
	if err != nil {
		t.Fatal(err)
	}
	b := make([]byte, HeaderBytes+len(record))
	binary.BigEndian.PutUint32(b[:4], 2<<8|1)
	copy(b[HeaderBytes:], record)
	path := filepath.Join(t.TempDir(), "r.0.0.mca")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := ReadChunk(path, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string([]byte{10, 0, 0, 0}) {
		t.Fatalf("got %v", got)
	}
}

func TestWriteChunkPersistsAndPreservesOtherChunk(t *testing.T) {
	one, _ := EncodeZlibRecord([]byte("one"))
	two, _ := EncodeZlibRecord([]byte("two"))
	b := make([]byte, HeaderBytes+2*SectorBytes)
	binary.BigEndian.PutUint32(b[0:4], 2<<8|1)
	binary.BigEndian.PutUint32(b[4:8], 3<<8|1)
	copy(b[HeaderBytes:], one)
	copy(b[HeaderBytes+SectorBytes:], two)
	path := filepath.Join(t.TempDir(), "r.0.0.mca")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := NewStore().WriteChunk(path, 0, 0, []byte("changed")); err != nil {
		t.Fatal(err)
	}
	got, err := ReadChunk(path, 0, 0)
	if err != nil || string(got) != "changed" {
		t.Fatalf("changed=%q err=%v", got, err)
	}
	got, err = ReadChunk(path, 1, 0)
	if err != nil || string(got) != "two" {
		t.Fatalf("preserved=%q err=%v", got, err)
	}
}

func TestWriteChunkAcceptsUnpaddedFinalSector(t *testing.T) {
	record, err := EncodeZlibRecord([]byte("before"))
	if err != nil {
		t.Fatal(err)
	}
	b := make([]byte, HeaderBytes+len(record))
	binary.BigEndian.PutUint32(b[:4], 2<<8|1)
	copy(b[HeaderBytes:], record)
	path := filepath.Join(t.TempDir(), "r.0.0.mca")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := NewStore().WriteChunk(path, 0, 0, []byte("after")); err != nil {
		t.Fatal(err)
	}
	got, err := ReadChunk(path, 0, 0)
	if err != nil || string(got) != "after" {
		t.Fatalf("got=%q err=%v", got, err)
	}
}
