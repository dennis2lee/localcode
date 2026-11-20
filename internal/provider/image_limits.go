package provider

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	// The decoders image.DecodeConfig reads a header with. Standard
	// library only: the default build stays pure Go.
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
)

// MaxImageBytes is the largest image a request carries. Bedrock's Converse
// API refuses an image over 3.75 MB, and the Anthropic API one over 10 MB
// once base64-encoded (5 MB on Bedrock). An image a server refuses stays
// in the history and is sent with every request after it, so the limit is
// the smallest of the wires rather than the largest.
const MaxImageBytes = 3_750_000

// MaxImageSide is the longest side, in pixels, of an image a request
// carries. The Anthropic API and Bedrock both refuse an image past 8000
// pixels on either side.
const MaxImageSide = 8000

// MaxImageBytesPerRequest caps the images one request carries, added
// together over the whole history. Bedrock refuses a request over 20 MB,
// and the images go as base64, a third larger than their bytes: 10 MB of
// images is about 14 MB on the wire, which leaves room for the text of a
// long conversation. The same figure as one message's cap, so a message
// that was accepted always fits in a request on its own.
const MaxImageBytesPerRequest = MaxImageBytesPerMessage

// ImageSize reads an image's width and height from its header, without
// decoding the pixels. ok is false when the header could not be read.
func ImageSize(data []byte) (width, height int, ok bool) {
	if w, h, isWebP, ok := webpSize(data); isWebP {
		return w, h, ok
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return 0, 0, false
	}
	return cfg.Width, cfg.Height, true
}

// ImageTooBig says why an image is past what every adapter's server takes,
// or "" when it is within it. An image whose size cannot be read is judged
// on its bytes alone: refusing what this cannot measure would refuse
// images a server takes.
func ImageTooBig(data []byte) string {
	if len(data) > MaxImageBytes {
		return fmt.Sprintf("it is %d bytes, over the %d byte limit for one image", len(data), MaxImageBytes)
	}
	if w, h, ok := ImageSize(data); ok && (w > MaxImageSide || h > MaxImageSide) {
		return fmt.Sprintf("it is %dx%d pixels, over the %d pixel limit for a side", w, h, MaxImageSide)
	}
	return ""
}

// webpSize reads a WebP file's canvas size. isWebP is false for anything
// that is not a RIFF WEBP file; ok is false for one whose first chunk is
// cut short or is not one of the three image chunks.
//
// The layouts are the WebP container's (RFC 9649): the first chunk is
// "VP8 " (lossy), "VP8L" (lossless) or "VP8X" (extended), and each stores
// the size in its own way.
func webpSize(b []byte) (w, h int, isWebP, ok bool) {
	if len(b) < 16 || string(b[0:4]) != "RIFF" || string(b[8:12]) != "WEBP" {
		return 0, 0, false, false
	}
	switch string(b[12:16]) {
	case "VP8 ":
		// A 3-byte frame tag, the start code 9d 01 2a, then 14 bits of
		// width and 14 of height, little-endian, each over 2 bytes.
		if len(b) < 30 || b[23] != 0x9d || b[24] != 0x01 || b[25] != 0x2a {
			return 0, 0, true, false
		}
		w = int(binary.LittleEndian.Uint16(b[26:28]) & 0x3fff)
		h = int(binary.LittleEndian.Uint16(b[28:30]) & 0x3fff)
		return w, h, true, true
	case "VP8L":
		// The signature byte 0x2f, then width-1 and height-1 in 14 bits
		// each, packed little-endian.
		if len(b) < 25 || b[20] != 0x2f {
			return 0, 0, true, false
		}
		bits := binary.LittleEndian.Uint32(b[21:25])
		w = int(bits&0x3fff) + 1
		h = int((bits>>14)&0x3fff) + 1
		return w, h, true, true
	case "VP8X":
		// Flags and three reserved bytes, then canvas width-1 and
		// height-1 in 24 bits each.
		if len(b) < 30 {
			return 0, 0, true, false
		}
		w = int(uint32(b[24])|uint32(b[25])<<8|uint32(b[26])<<16) + 1
		h = int(uint32(b[27])|uint32(b[28])<<8|uint32(b[29])<<16) + 1
		return w, h, true, true
	}
	return 0, 0, true, false
}
