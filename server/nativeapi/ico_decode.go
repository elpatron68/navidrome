package nativeapi

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
)

func isICO(data []byte) bool {
	return len(data) >= 6 && data[0] == 0 && data[1] == 0 && data[2] == 1 && data[3] == 0
}

func decodeICOToPNG(data []byte) (io.Reader, error) {
	img, err := decodeICO(data)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, fmt.Errorf("encoding ICO as PNG: %w", err)
	}
	return bytes.NewReader(buf.Bytes()), nil
}

type icoDirEntry struct {
	width, height uint16
	bpp           uint16
	size, offset  uint32
}

func decodeICO(data []byte) (image.Image, error) {
	if !isICO(data) {
		return nil, errors.New("not an ICO file")
	}
	count := binary.LittleEndian.Uint16(data[4:6])
	if count == 0 {
		return nil, errors.New("empty ICO")
	}

	var best icoDirEntry
	var bestArea int
	const headerSize = 6
	const entrySize = 16
	for i := 0; i < int(count); i++ {
		off := headerSize + i*entrySize
		if off+entrySize > len(data) {
			return nil, errors.New("invalid ICO directory")
		}
		entry := parseICOEntry(data[off : off+entrySize])
		w, h := icoDimension(entry.width), icoDimension(entry.height)
		if area := w * h; area > bestArea {
			best = entry
			bestArea = area
		}
	}
	if bestArea == 0 {
		return nil, errors.New("no ICO images found")
	}
	return decodeICOImage(data, best)
}

func icoDimension(v uint16) int {
	if v == 0 {
		return 256
	}
	return int(v)
}

func parseICOEntry(b []byte) icoDirEntry {
	return icoDirEntry{
		width:  uint16(b[0]),
		height: uint16(b[1]),
		bpp:    binary.LittleEndian.Uint16(b[6:8]),
		size:   binary.LittleEndian.Uint32(b[8:12]),
		offset: binary.LittleEndian.Uint32(b[12:16]),
	}
}

func decodeICOImage(data []byte, entry icoDirEntry) (image.Image, error) {
	if int(entry.offset)+int(entry.size) > len(data) {
		return nil, errors.New("ICO image out of bounds")
	}
	chunk := data[entry.offset : entry.offset+entry.size]
	if len(chunk) >= 8 && chunk[0] == 0x89 && chunk[1] == 'P' && chunk[2] == 'N' && chunk[3] == 'G' {
		return png.Decode(bytes.NewReader(chunk))
	}
	return decodeICODIB(chunk, icoDimension(entry.width), icoDimension(entry.height))
}

func decodeICODIB(data []byte, width, height int) (image.Image, error) {
	if len(data) < 40 {
		return nil, errors.New("DIB too short")
	}
	if binary.LittleEndian.Uint32(data[0:4]) != 40 {
		return nil, errors.New("unsupported DIB header size")
	}

	biWidth := int(int32(binary.LittleEndian.Uint32(data[4:8])))
	biHeight := int(int32(binary.LittleEndian.Uint32(data[8:12])))
	iconHeight := biHeight / 2
	if iconHeight <= 0 {
		iconHeight = height
	}
	if width <= 0 {
		width = biWidth
	}
	if width <= 0 || iconHeight <= 0 {
		return nil, errors.New("invalid ICO dimensions")
	}

	bpp := int(binary.LittleEndian.Uint16(data[14:16]))
	offset := 40
	img := image.NewRGBA(image.Rect(0, 0, width, iconHeight))

	switch bpp {
	case 8:
		numColors := int(binary.LittleEndian.Uint32(data[32:36]))
		if numColors == 0 {
			numColors = 256
		}
		palette := make([]color.RGBA, numColors)
		for i := 0; i < numColors; i++ {
			if offset+4 > len(data) {
				return nil, errors.New("truncated ICO palette")
			}
			palette[i] = color.RGBA{R: data[offset+2], G: data[offset+1], B: data[offset], A: 255}
			offset += 4
		}
		rowSize := ((width + 3) / 4) * 4
		for y := 0; y < iconHeight; y++ {
			srcY := iconHeight - 1 - y
			rowStart := offset + srcY*rowSize
			if rowStart+width > len(data) {
				return nil, errors.New("truncated ICO bitmap")
			}
			for x := 0; x < width; x++ {
				img.Set(x, y, palette[data[rowStart+x]])
			}
		}
		maskOffset := offset + rowSize*iconHeight
		maskRowSize := ((width + 31) / 32) * 4
		for y := 0; y < iconHeight; y++ {
			srcY := iconHeight - 1 - y
			rowStart := maskOffset + srcY*maskRowSize
			for x := 0; x < width; x++ {
				byteIdx := x / 8
				bitIdx := 7 - (x % 8)
				if rowStart+byteIdx >= len(data) {
					continue
				}
				if data[rowStart+byteIdx]&(1<<bitIdx) != 0 {
					img.Set(x, y, color.RGBA{A: 0})
				}
			}
		}
	case 24:
		rowSize := ((width*3 + 3) / 4) * 4
		for y := 0; y < iconHeight; y++ {
			srcY := iconHeight - 1 - y
			rowStart := offset + srcY*rowSize
			for x := 0; x < width; x++ {
				p := rowStart + x*3
				if p+3 > len(data) {
					return nil, errors.New("truncated ICO bitmap")
				}
				img.Set(x, y, color.RGBA{R: data[p+2], G: data[p+1], B: data[p], A: 255})
			}
		}
	case 32:
		rowSize := width * 4
		for y := 0; y < iconHeight; y++ {
			srcY := iconHeight - 1 - y
			rowStart := offset + srcY*rowSize
			for x := 0; x < width; x++ {
				p := rowStart + x*4
				if p+4 > len(data) {
					return nil, errors.New("truncated ICO bitmap")
				}
				img.Set(x, y, color.RGBA{R: data[p+2], G: data[p+1], B: data[p], A: data[p+3]})
			}
		}
	default:
		return nil, fmt.Errorf("unsupported ICO bit depth: %d", bpp)
	}

	return img, nil
}
