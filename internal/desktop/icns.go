package desktop

import (
	"bytes"
	"encoding/binary"
	"fmt"
)

type icnsEntrySpec struct {
	osType string
	size   int
}

var standardICNSEntries = [4]icnsEntrySpec{
	{osType: "ic07", size: 128},  // 128x128 PNG
	{osType: "ic08", size: 256},  // 256x256 PNG (128x128@2x)
	{osType: "ic09", size: 512},  // 512x512 PNG (256x256@2x)
	{osType: "ic10", size: 1024}, // 1024x1024 PNG (512x512@2x Retina)
}

// EncodeColabHiveICNS builds a standard Apple macOS `.icns` multi-resolution icon container
// (`'icns'` header + `'ic07'` 128px, `'ic08'` 256px, `'ic09'` 512px, `'ic10'` 1024px PNG chunks)
// in 100% pure Go without requiring macOS `iconutil`.
func EncodeColabHiveICNS() ([]byte, error) {
	type encodedChunk struct {
		osType string
		png    []byte
	}
	chunks := make([]encodedChunk, 0, len(standardICNSEntries))
	totalBytes := uint32(8) // 'icns' (4) + total file length (4)

	for _, spec := range standardICNSEntries {
		pngBytes, err := EncodeColabHivePNG(spec.size)
		if err != nil {
			return nil, fmt.Errorf("encode %s (%dpx) PNG: %w", spec.osType, spec.size, err)
		}
		chunks = append(chunks, encodedChunk{
			osType: spec.osType,
			png:    pngBytes,
		})
		totalBytes += uint32(8 + len(pngBytes))
	}

	var out bytes.Buffer
	out.Grow(int(totalBytes))
	out.WriteString("icns")
	var sizeBuf [4]byte
	binary.BigEndian.PutUint32(sizeBuf[:], totalBytes)
	out.Write(sizeBuf[:])

	for _, ch := range chunks {
		out.WriteString(ch.osType)
		binary.BigEndian.PutUint32(sizeBuf[:], uint32(8+len(ch.png)))
		out.Write(sizeBuf[:])
		out.Write(ch.png)
	}
	return out.Bytes(), nil
}
