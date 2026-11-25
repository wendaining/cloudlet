package siteartifact

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
)

const (
	directoryHeaderSize = 46
	directoryEndSize    = 22
	zip64LocatorSize    = 20
	zip64EndSize        = 56
)

type directoryEnd struct {
	position int64
	count    uint64
	size     uint64
	offset   uint64
}

// checkDirectoryLimit bounds the work archive/zip may do before it returns
// its list of entries. The standard reader still parses and validates ZIP
// metadata and contents. Count actual headers as well as the reported count.
func checkDirectoryLimit(archive io.ReaderAt, size int64, limit int) error {
	end, err := readDirectoryEnd(archive, size)
	if err != nil {
		return err
	}
	if end.count > uint64(limit) {
		return fmt.Errorf("%w: archive exceeds the limit of %d entries", ErrInvalidArchive, limit)
	}
	if end.size > uint64(end.position) || end.offset > uint64(size) {
		return zip.ErrFormat
	}
	start := end.position - int64(end.size)
	if err := checkDirectoryHeaders(archive, size, start, limit); err != nil {
		return err
	}
	// archive/zip can fall back to the declared offset when it finds a
	// valid header there and the archive has a positive base offset.
	if declared := int64(end.offset); declared < start {
		return checkDirectoryHeaders(archive, size, declared, limit)
	}
	return nil
}

func readDirectoryEnd(archive io.ReaderAt, size int64) (directoryEnd, error) {
	// Use the same search window as archive/zip to locate the end record.
	tail := make([]byte, min(size, 65*1024))
	start := size - int64(len(tail))
	if _, err := archive.ReadAt(tail, start); err != nil {
		return directoryEnd{}, err
	}
	if len(tail) < directoryEndSize {
		return directoryEnd{}, zip.ErrFormat
	}
	index := bytes.LastIndex(tail[:len(tail)-directoryEndSize+4], []byte("PK\x05\x06"))
	if index < 0 {
		return directoryEnd{}, zip.ErrFormat
	}
	record := tail[index:]
	if int(binary.LittleEndian.Uint16(record[20:22])) > len(record)-directoryEndSize {
		return directoryEnd{}, zip.ErrFormat
	}
	end := directoryEnd{
		position: start + int64(index),
		count:    uint64(binary.LittleEndian.Uint16(record[10:12])),
		size:     uint64(binary.LittleEndian.Uint32(record[12:16])),
		offset:   uint64(binary.LittleEndian.Uint32(record[16:20])),
	}
	if end.count == 0xffff || end.size == 0xffffffff || end.offset == 0xffffffff {
		return readZIP64End(archive, end)
	}
	return end, nil
}

func readZIP64End(archive io.ReaderAt, end directoryEnd) (directoryEnd, error) {
	locatorOffset := end.position - zip64LocatorSize
	if locatorOffset < 0 {
		return end, nil
	}
	var locator [zip64LocatorSize]byte
	if _, err := archive.ReadAt(locator[:], locatorOffset); err != nil {
		return directoryEnd{}, err
	}
	if string(locator[:4]) != "PK\x06\x07" || binary.LittleEndian.Uint32(locator[4:8]) != 0 ||
		binary.LittleEndian.Uint32(locator[16:20]) != 1 {
		return end, nil
	}
	offset := binary.LittleEndian.Uint64(locator[8:16])
	if locatorOffset < zip64EndSize || offset > uint64(locatorOffset-zip64EndSize) {
		return directoryEnd{}, zip.ErrFormat
	}
	var record [zip64EndSize]byte
	if _, err := archive.ReadAt(record[:], int64(offset)); err != nil {
		return directoryEnd{}, err
	}
	if string(record[:4]) != "PK\x06\x06" {
		return directoryEnd{}, zip.ErrFormat
	}
	return directoryEnd{
		position: int64(offset),
		count:    binary.LittleEndian.Uint64(record[32:40]),
		size:     binary.LittleEndian.Uint64(record[40:48]),
		offset:   binary.LittleEndian.Uint64(record[48:56]),
	}, nil
}

func checkDirectoryHeaders(archive io.ReaderAt, size, offset int64, limit int) error {
	var header [directoryHeaderSize]byte
	for count := 0; offset <= size-directoryHeaderSize; count++ {
		if _, err := archive.ReadAt(header[:], offset); err != nil {
			return err
		}
		if string(header[:4]) != "PK\x01\x02" {
			return nil
		}
		if count == limit {
			return fmt.Errorf("%w: archive exceeds the limit of %d entries", ErrInvalidArchive, limit)
		}
		name := binary.LittleEndian.Uint16(header[28:30])
		extra := binary.LittleEndian.Uint16(header[30:32])
		comment := binary.LittleEndian.Uint16(header[32:34])
		length := directoryHeaderSize + int64(name) + int64(extra) + int64(comment)
		if length > size-offset {
			return nil // archive/zip handles truncated entry metadata.
		}
		offset += length
	}
	return nil
}
