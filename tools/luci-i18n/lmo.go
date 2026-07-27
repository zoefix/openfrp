package main

import (
	"encoding/binary"
	"fmt"
	"os"
	"sort"
)

// The .lmo layout, from LuCI's template_lmo.h:
//
//	[ string data, each entry padded to a 4-byte boundary ]
//	[ index: one 16-byte entry per string, sorted by key hash ]
//	[ uint32: byte offset at which the index starts           ]
//
// Every integer is big endian. Each index entry is:
//
//	uint32 key_id   hash of the msgid
//	uint32 val_id   hash of the msgstr
//	uint32 offset   where the msgstr data begins
//	uint32 length   its length in bytes
const entrySize = 16

type entry struct {
	keyID  uint32
	valID  uint32
	offset uint32
	length uint32
}

// sfhHash is Paul Hsieh's SuperFastHash, which is what LuCI keys entries by.
//
// The implementation has to match byte for byte. It was checked against a
// stock catalogue pulled off a live router: hashing "Save", "Hostname",
// "Interface", "Password", "Reboot" and "Firewall" finds each one's Chinese
// translation in LuCI's own base.zh-cn.lmo. A hash that is merely close
// produces a catalogue that loads without error and translates nothing.
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
		keyID := sfhHash([]byte(msg.id))

		// A collision would make one of the two strings untranslatable at
		// random, so refuse rather than produce a subtly wrong catalogue.
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
		// Entries are padded so every offset stays 4-byte aligned.
		for len(data)%4 != 0 {
			data = append(data, 0)
		}
	}

	// The lookup is a binary search, so the index must be sorted by key.
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

// readCatalogue returns a catalogue's raw bytes together with its index.
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

// dump decodes a catalogue, which is how the format was validated against a
// stock LuCI .lmo before the encoder above was trusted.
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

// lookupOne resolves a single msgid, reporting whether it was present.
func lookupOne(raw, index []byte, msgid string) (string, bool) {
	want := sfhHash([]byte(msgid))

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

// lookup resolves msgids against a catalogue. This is the check that decides
// whether the hash implementation is right, so it is a first-class command
// rather than a test fixture.
func lookup(path string, msgids []string) error {
	raw, index, err := readCatalogue(path)
	if err != nil {
		return err
	}

	for _, msgid := range msgids {
		if found, ok := lookupOne(raw, index, msgid); ok {
			fmt.Printf("  HIT  %08x  %q -> %q\n", sfhHash([]byte(msgid)), msgid, found)
		} else {
			fmt.Printf("  MISS %08x  %q\n", sfhHash([]byte(msgid)), msgid)
		}
	}
	return nil
}
