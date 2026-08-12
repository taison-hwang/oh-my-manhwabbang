package rar4

import (
	"bytes"
	"strings"
	"unicode/utf16"

	"shelf/internal/kenc"
)

// RAR4 stores an entry name in one of three shapes, and which one it is
// decides whether this product's encoding machinery is involved at all.
//
//  1. flagged Unicode, with an encoded companion — NAME_SIZE covers
//     `<OEM name> NUL <encoded UTF-16>`. The companion is authoritative and
//     [decodeEncodedName] reconstructs it exactly; the OEM prefix is only the
//     fallback the producer left for old readers.
//  2. flagged Unicode, no companion — the field is plain UTF-8.
//  3. not flagged — the field is raw bytes in whatever code page the packer
//     ran under, which in this collection is CP949. This is the ZIP situation
//     exactly, so it goes through kenc and inherits arch §4.4 whole, including
//     the per-archive Shift_JIS fallback of [resolveArchiveNames].
//
// The reference collection is entirely shape 1: all 14 archives set the flag
// and carry a companion, and the OEM prefixes are CP949. Shapes 2 and 3 are
// implemented because a RAR from anywhere else will use them, not because a
// file here does.
func decodeName(raw []byte, unicodeFlag bool) (name, enc string) {
	if unicodeFlag {
		if i := bytes.IndexByte(raw, 0); i >= 0 && i+1 < len(raw) {
			if s, ok := decodeEncodedName(raw[:i], raw[i+1:]); ok {
				return normalizeSeparators(s), kenc.EncUTF8
			}
			// The companion did not decode. The OEM prefix is still a real
			// name, so fall through to it rather than lose the entry.
			name, enc = kenc.DecodeEntryName(raw[:i], false)
			return normalizeSeparators(name), enc
		}
		// No companion: the field itself is UTF-8. kenc still owns the
		// "producer set the flag and lied" case (EncUTF8Invalid).
		name, enc = kenc.DecodeEntryName(trimNUL(raw), true)
		return normalizeSeparators(name), enc
	}
	name, enc = kenc.DecodeEntryName(trimNUL(raw), false)
	return normalizeSeparators(name), enc
}

// legacyBytes returns the portion of a raw name that a legacy code page could
// read, or nil when the name is not legacy-encoded evidence.
//
// [resolveArchiveNames] uses it the way zipidx uses Entry.RawName: an entry
// whose companion decoded is already correct and must not vote on, or be
// rewritten by, an archive-wide encoding guess.
func legacyBytes(raw []byte, unicodeFlag bool) []byte {
	if !unicodeFlag {
		return trimNUL(raw)
	}
	if i := bytes.IndexByte(raw, 0); i >= 0 && i+1 < len(raw) {
		return raw[:i]
	}
	return trimNUL(raw)
}

func trimNUL(raw []byte) []byte {
	if i := bytes.IndexByte(raw, 0); i >= 0 {
		return raw[:i]
	}
	return raw
}

// decodeEncodedName reconstructs the UTF-16 name RAR packs alongside the OEM
// one.
//
// The encoding is a two-bit opcode stream, described here because there is no
// specification to point at — this is the format unrar's DecodeFileName
// implements, and the code below is a transliteration of it:
//
//	byte 0     the high byte shared by most characters in the name
//	then, two bits at a time, most significant pair first:
//	  0  one byte follows: a character in U+0000..U+00FF
//	  1  one byte follows: that byte with the shared high byte above it
//	  2  two bytes follow: a little-endian UTF-16 code unit
//	  3  a run, copied from the OEM name rather than spelled out:
//	       length byte; if its top bit is set, a correction byte follows and
//	       each OEM byte is (oem+correction)&0xFF under the shared high byte,
//	       otherwise the OEM bytes are copied as-is. Length is +2 either way.
//
// Opcode 3 is why the OEM prefix has to be passed in: a run refers to it by
// the position already decoded, so the two names are decoded in lockstep.
//
// ok is false when the stream runs off either buffer, which means the name is
// not what its header claims and the caller should fall back rather than
// return a half-decoded string.
func decodeEncodedName(oem, enc []byte) (string, bool) {
	if len(enc) == 0 {
		return "", false
	}
	// The decoded name cannot be longer than the opcode stream can address:
	// every character costs at least two bits, and a run costs at least a
	// length byte. This bounds the allocation on untrusted input.
	const maxName = 4096
	high := uint16(enc[0]) << 8
	out := make([]uint16, 0, len(oem)+len(enc))

	encPos := 1
	var flags byte
	var flagBits int

	for encPos < len(enc) && len(out) < maxName {
		if flagBits == 0 {
			flags = enc[encPos]
			encPos++
			flagBits = 8
			if encPos >= len(enc) {
				break
			}
		}
		flagBits -= 2
		switch (flags >> flagBits) & 3 {
		case 0:
			out = append(out, uint16(enc[encPos]))
			encPos++
		case 1:
			out = append(out, uint16(enc[encPos])|high)
			encPos++
		case 2:
			if encPos+1 >= len(enc) {
				return "", false
			}
			out = append(out, uint16(enc[encPos])|uint16(enc[encPos+1])<<8)
			encPos += 2
		case 3:
			length := int(enc[encPos])
			encPos++
			if length&0x80 != 0 {
				if encPos >= len(enc) {
					return "", false
				}
				correction := enc[encPos]
				encPos++
				for n := (length & 0x7F) + 2; n > 0 && len(out) < maxName; n-- {
					if len(out) >= len(oem) {
						return "", false
					}
					out = append(out, uint16((oem[len(out)]+correction)&0xFF)|high)
				}
			} else {
				for n := length + 2; n > 0 && len(out) < maxName; n-- {
					if len(out) >= len(oem) {
						return "", false
					}
					out = append(out, uint16(oem[len(out)]))
				}
			}
		}
	}
	if len(out) == 0 {
		return "", false
	}
	return string(utf16.Decode(out)), true
}

// normalizeSeparators turns RAR's `\` into the `/` every other layer assumes.
//
// It runs on the decoded string and never on the raw bytes, and that ordering
// is load-bearing: a Shift_JIS trailing byte may be 0x5C, so replacing on
// bytes would cut a kanji in half and invent a directory level. After
// decoding, a `\` is a real backslash character or it is not there.
func normalizeSeparators(s string) string {
	if !strings.ContainsRune(s, '\\') {
		return s
	}
	return strings.ReplaceAll(s, "\\", "/")
}
