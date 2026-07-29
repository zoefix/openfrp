package main

import (
	"encoding/binary"
	"fmt"
	"os"
	"sort"
	"strings"
	"unicode"
)

const entrySize = 16

type entry struct {
	keyID  uint32
	valID  uint32
	offset uint32
	length uint32
}

func hashKey(msgid string) uint32 {
	return sfhHash([]byte(collapseWhitespace(msgid)))
}

func collapseWhitespace(msgid string) string {
	trimmed := strings.TrimFunc(msgid, func(r rune) bool {
		return unicode.IsSpace(r) || r == '\ufeff'
	})

	var out strings.Builder
	out.Grow(len(trimmed))

	space := false
	for _, r := range trimmed {
		if r == ' ' || r == '\t' || r == '\n' {
			space = true
			continue
		}
		if space {
			out.WriteByte(' ')
			space = false
		}
		out.WriteRune(r)
	}
	return out.String()
}

func sfhHash(data []byte) uint32 {
	length := len(data)
	if length <= 0 {
		return 0
	}

	hash := uint32(length)
	rem := length & 3
	blocks := length >> 2
	pos := 0

	get16 := func(i int) uint32 {
		return uint32(data[i]) | uint32(data[i+1])<<8
	}

	for ; blocks > 0; blocks-- {
		hash += get16(pos)
		tmp := (get16(pos+2) << 11) ^ hash
		hash = (hash << 16) ^ tmp
		pos += 4
		hash += hash >> 11
	}

	switch rem {
	case 3:
		hash += get16(pos)
		hash ^= hash << 16
		hash ^= uint32(data[pos+2]) << 18
		hash += hash >> 11
	case 2:
		hash += get16(pos)
		hash ^= hash << 11
		hash += hash >> 17
	case 1:
		hash += uint32(data[pos])
		hash ^= hash << 10
		hash += hash >> 1
	}

	hash ^= hash << 3
	hash += hash >> 5
	hash ^= hash << 4
	hash += hash >> 17
	hash ^= hash << 25
	hash += hash >> 6

	return hash
}

func compile(input, output string) error {
	messages, err := parsePO(input)
	if err != nil {
		return fmt.Errorf("parse %s: %w", input, err)
	}
	if len(messages) == 0 {
		return fmt.Errorf("%s contains no translated messages", input)
	}

	var (
		data    []byte
		entries []entry
		seen    = map[uint32]string{}
	)

	for _, msg := range messages {
		keyID := hashKey(msg.id)

		if previous, clash := seen[keyID]; clash && previous != msg.id {
			return fmt.Errorf("hash collision between %q and %q", previous, msg.id)
		}
		seen[keyID] = msg.id

		entries = append(entries, entry{
			keyID:  keyID,
			valID:  sfhHash([]byte(msg.str)),
			offset: uint32(len(data)),
			length: uint32(len(msg.str)),
		})

		data = append(data, msg.str...)

		for len(data)%4 != 0 {
			data = append(data, 0)
		}
	}

	sort.Slice(entries, func(i, j int) bool { return entries[i].keyID < entries[j].keyID })

	indexOffset := uint32(len(data))
	out := make([]byte, 0, len(data)+len(entries)*entrySize+4)
	out = append(out, data...)

	for _, e := range entries {
		out = binary.BigEndian.AppendUint32(out, e.keyID)
		out = binary.BigEndian.AppendUint32(out, e.valID)
		out = binary.BigEndian.AppendUint32(out, e.offset)
		out = binary.BigEndian.AppendUint32(out, e.length)
	}
	out = binary.BigEndian.AppendUint32(out, indexOffset)

	if err := os.WriteFile(output, out, 0o644); err != nil {
		return err
	}

	fmt.Printf("%s: %d messages, %d bytes\n", output, len(entries), len(out))
	return nil
}

func readCatalogue(path string) (raw, index []byte, err error) {
	raw, err = os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	if len(raw) < 4 {
		return nil, nil, fmt.Errorf("%s is too short to be an lmo", path)
	}

	indexOffset := binary.BigEndian.Uint32(raw[len(raw)-4:])
	if int(indexOffset) > len(raw)-4 {
		return nil, nil, fmt.Errorf("index offset %d is past the end of a %d byte file",
			indexOffset, len(raw))
	}

	index = raw[indexOffset : len(raw)-4]
	if len(index)%entrySize != 0 {
		return nil, nil, fmt.Errorf("index is %d bytes, not a multiple of %d",
			len(index), entrySize)
	}
	return raw, index, nil
}

func dump(path string) error {
	raw, index, err := readCatalogue(path)
	if err != nil {
		return err
	}

	count := len(index) / entrySize
	fmt.Printf("%s: %d entries\n", path, count)

	for i := 0; i < count && i < 8; i++ {
		base := i * entrySize
		e := entry{
			keyID:  binary.BigEndian.Uint32(index[base:]),
			offset: binary.BigEndian.Uint32(index[base+8:]),
			length: binary.BigEndian.Uint32(index[base+12:]),
		}
		if int(e.offset+e.length) > len(raw) {
			return fmt.Errorf("entry %d points outside the file", i)
		}
		fmt.Printf("  key=%08x len=%3d %q\n",
			e.keyID, e.length, string(raw[e.offset:e.offset+e.length]))
	}

	return nil
}

func lookupOne(raw, index []byte, msgid string) (string, bool) {
	want := hashKey(msgid)

	for i := range len(index) / entrySize {
		base := i * entrySize
		if binary.BigEndian.Uint32(index[base:]) != want {
			continue
		}
		offset := binary.BigEndian.Uint32(index[base+8:])
		length := binary.BigEndian.Uint32(index[base+12:])
		return string(raw[offset : offset+length]), true
	}
	return "", false
}

func lookup(path string, msgids []string) error {
	raw, index, err := readCatalogue(path)
	if err != nil {
		return err
	}

	for _, msgid := range msgids {
		if found, ok := lookupOne(raw, index, msgid); ok {
			fmt.Printf("  HIT  %08x  %q -> %q\n", hashKey(msgid), msgid, found)
		} else {
			fmt.Printf("  MISS %08x  %q\n", hashKey(msgid), msgid)
		}
	}
	return nil
}
