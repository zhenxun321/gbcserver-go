package imageutil

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"testing"
)

func TestCompressPNGResizesAndKeepsValidPNG(t *testing.T) {
	source := image.NewRGBA(image.Rect(0, 0, 800, 400))
	for y := 0; y < 400; y++ {
		for x := 0; x < 800; x++ {
			source.SetRGBA(x, y, color.RGBA{R: uint8(x % 255), G: uint8(y % 255), B: 120, A: 255})
		}
	}
	var input bytes.Buffer
	if err := png.Encode(&input, source); err != nil {
		t.Fatal(err)
	}
	compressed, changed, err := Compress(input.Bytes(), ".png", Options{MaxDimension: 256})
	if err != nil || !changed {
		t.Fatalf("PNG 应被压缩: changed=%v err=%v", changed, err)
	}
	width, height, err := DecodeDimensions(compressed)
	if err != nil || width > 256 || height > 128 || width*2 != height*4 {
		t.Fatalf("压缩尺寸错误: %dx%d err=%v", width, height, err)
	}
}

func TestCompressPreservesAlreadyCompressedFormats(t *testing.T) {
	webp := []byte("RIFF0000WEBPVP8 ")
	compressed, changed, err := Compress(webp, ".webp", Options{MaxDimension: 256})
	if err != nil || changed || !bytes.Equal(compressed, webp) {
		t.Fatalf("WebP 应原样保留: changed=%v err=%v", changed, err)
	}
	gif := []byte("GIF89a")
	compressed, changed, err = Compress(gif, ".gif", Options{MaxDimension: 256})
	if err != nil || changed || !bytes.Equal(compressed, gif) {
		t.Fatalf("GIF 应原样保留: changed=%v err=%v", changed, err)
	}
}
