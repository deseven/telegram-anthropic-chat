// Package images handles resizing and re-encoding incoming Telegram photos
// before sending them to Anthropic.
//
// For each photo:
//   - if width or height exceeds 1024px, resize so the largest dimension
//     becomes 1024px, keeping the original aspect ratio;
//   - convert to JPEG at 85% quality;
//   - return the base64-encoded bytes.
package images

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/jpeg"
	_ "image/png"

	// Register GIF decoding for animated stickers / gifs sent as photos.
	_ "image/gif"

	"golang.org/x/image/draw"
)

const maxDim = 1024
const jpegQuality = 85

// Process decodes raw image bytes, resizes if needed, re-encodes as JPEG at
// 85% quality and returns the base64-encoded result.
func Process(raw []byte) (string, error) {
	src, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return "", fmt.Errorf("decode image: %w", err)
	}

	bounds := src.Bounds()
	w, h := bounds.Dx(), bounds.Dy()

	if w > maxDim || h > maxDim {
		src = resize(src, w, h)
	}

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, src, &jpeg.Options{Quality: jpegQuality}); err != nil {
		return "", fmt.Errorf("encode jpeg: %w", err)
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes()), nil
}

func resize(src image.Image, w, h int) image.Image {
	scale := float64(maxDim) / float64(max(w, h))
	nw := max(1, int(float64(w)*scale))
	nh := max(1, int(float64(h)*scale))
	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, src.Bounds(), draw.Over, nil)
	return dst
}
