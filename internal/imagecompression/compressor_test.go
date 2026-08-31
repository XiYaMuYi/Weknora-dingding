package imagecompression

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"

	genwebp "github.com/gen2brain/webp"
	"golang.org/x/image/bmp"
	"golang.org/x/image/tiff"
)

func TestCompressLargeJPEGMeetsHardLimit(t *testing.T) {
	t.Parallel()
	src := noisyNRGBA(2200, 1800, false)
	var input bytes.Buffer
	if err := jpeg.Encode(&input, src, &jpeg.Options{Quality: 100}); err != nil {
		t.Fatal(err)
	}
	if input.Len() <= DefaultConfig().TargetBytes {
		t.Fatalf("fixture must exceed target: got %d", input.Len())
	}

	got, err := Compress(input.Bytes(), "scan.jpg", DefaultConfig())
	if err != nil {
		t.Fatalf("Compress() error = %v", err)
	}
	if len(got.Data) > DefaultConfig().TargetBytes {
		t.Fatalf("compressed size = %d, target = %d", len(got.Data), DefaultConfig().TargetBytes)
	}
	if got.Format != "webp" || got.ContentType != "image/webp" || !strings.HasSuffix(got.FileName, ".webp") {
		t.Fatalf("unexpected output identity: format=%q contentType=%q fileName=%q", got.Format, got.ContentType, got.FileName)
	}
	if !got.Compressed || got.OriginalSize != int64(input.Len()) || got.CompressedSize != int64(len(got.Data)) {
		t.Fatalf("unexpected result metadata: %+v", got)
	}
	if got.OriginalHash == "" || got.StoredHash == "" || got.OriginalHash == got.StoredHash {
		t.Fatalf("unexpected hashes: original=%q stored=%q", got.OriginalHash, got.StoredHash)
	}
}

func TestCompressTransparentPNGPreservesAlpha(t *testing.T) {
	t.Parallel()
	src := noisyNRGBA(700, 700, true)
	var input bytes.Buffer
	if err := png.Encode(&input, src); err != nil {
		t.Fatal(err)
	}

	got, err := Compress(input.Bytes(), "transparent.png", DefaultConfig())
	if err != nil {
		t.Fatalf("Compress() error = %v", err)
	}
	decoded, format, err := image.Decode(bytes.NewReader(got.Data))
	if err != nil {
		t.Fatalf("decode compressed output: %v", err)
	}
	if format != "webp" {
		t.Fatalf("format = %q, want webp", format)
	}
	_, _, _, alpha := decoded.At(0, 0).RGBA()
	if alpha > 0x0800 {
		t.Fatalf("alpha at transparent pixel = %d, want near zero", alpha)
	}
}

func TestCompressAnimatedGIFPreservesAnimation(t *testing.T) {
	t.Parallel()
	palette := color.Palette{color.Black, color.White, color.RGBA{R: 255, A: 255}}
	frames := make([]*image.Paletted, 2)
	for frameIndex := range frames {
		frame := image.NewPaletted(image.Rect(0, 0, 900, 900), palette)
		for i := range frame.Pix {
			frame.Pix[i] = uint8((i + frameIndex) % len(palette))
		}
		frames[frameIndex] = frame
	}
	var input bytes.Buffer
	if err := gif.EncodeAll(&input, &gif.GIF{Image: frames, Delay: []int{7, 11}, LoopCount: 3}); err != nil {
		t.Fatal(err)
	}
	cfg := DefaultConfig()
	cfg.MinBytes = 0

	got, err := Compress(input.Bytes(), "animated.gif", cfg)
	if err != nil {
		t.Fatalf("Compress() error = %v", err)
	}
	decoded, err := gif.DecodeAll(bytes.NewReader(got.Data))
	if err != nil {
		t.Fatalf("decode compressed gif: %v", err)
	}
	if len(decoded.Image) != 2 || decoded.LoopCount != 3 {
		t.Fatalf("animation metadata lost: frames=%d loop=%d", len(decoded.Image), decoded.LoopCount)
	}
	if decoded.Delay[0] != 7 || decoded.Delay[1] != 11 {
		t.Fatalf("animation delays = %v", decoded.Delay)
	}
}

func TestCompressSmallImageKeepsOriginal(t *testing.T) {
	t.Parallel()
	var input bytes.Buffer
	if err := png.Encode(&input, image.NewNRGBA(image.Rect(0, 0, 16, 16))); err != nil {
		t.Fatal(err)
	}

	got, err := Compress(input.Bytes(), "icon.png", DefaultConfig())
	if err != nil {
		t.Fatalf("Compress() error = %v", err)
	}
	if got.Compressed || !bytes.Equal(got.Data, input.Bytes()) || got.FileName != "icon.png" {
		t.Fatalf("small input was changed: %+v", got)
	}
}

func TestCompressBMPAndTIFFInputs(t *testing.T) {
	t.Parallel()
	src := noisyNRGBA(700, 700, false)
	tests := []struct {
		name   string
		ext    string
		encode func(*bytes.Buffer) error
	}{
		{name: "bmp", ext: ".bmp", encode: func(output *bytes.Buffer) error { return bmp.Encode(output, src) }},
		{name: "tiff", ext: ".tiff", encode: func(output *bytes.Buffer) error { return tiff.Encode(output, src, nil) }},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			var input bytes.Buffer
			if err := test.encode(&input); err != nil {
				t.Fatal(err)
			}
			got, err := Compress(input.Bytes(), "scan"+test.ext, DefaultConfig())
			if err != nil {
				t.Fatalf("Compress() error = %v", err)
			}
			if got.Format != "webp" || !got.Compressed || len(got.Data) > DefaultConfig().TargetBytes {
				t.Fatalf("unexpected result: %+v", got)
			}
		})
	}
}

func TestCompressWebPInput(t *testing.T) {
	t.Parallel()
	src := noisyNRGBA(900, 900, false)
	var input bytes.Buffer
	if err := genwebp.Encode(&input, src, genwebp.Options{Quality: 100, Method: 4}); err != nil {
		t.Fatal(err)
	}
	cfg := DefaultConfig()
	cfg.MinBytes = 0
	got, err := Compress(input.Bytes(), "camera.webp", cfg)
	if err != nil {
		t.Fatalf("Compress() error = %v", err)
	}
	if got.Format != "webp" || len(got.Data) > cfg.TargetBytes {
		t.Fatalf("unexpected WebP result: %+v", got)
	}
}

func TestCompressSingleFrameGIFRemainsCorrectlyLabeled(t *testing.T) {
	t.Parallel()
	frame := image.NewPaletted(image.Rect(0, 0, 400, 400), color.Palette{color.Black, color.White})
	var input bytes.Buffer
	if err := gif.EncodeAll(&input, &gif.GIF{Image: []*image.Paletted{frame}, Delay: []int{5}}); err != nil {
		t.Fatal(err)
	}
	cfg := DefaultConfig()
	cfg.MinBytes = 0
	got, err := Compress(input.Bytes(), "one.gif", cfg)
	if err != nil {
		t.Fatalf("Compress() error = %v", err)
	}
	if got.Format != "gif" || !strings.HasSuffix(got.FileName, ".gif") {
		t.Fatalf("GIF identity mismatch: %+v", got)
	}
	if _, err := gif.DecodeAll(bytes.NewReader(got.Data)); err != nil {
		t.Fatalf("stored GIF is invalid: %v", err)
	}
}

func TestCompressRejectsOversizedSVG(t *testing.T) {
	t.Parallel()
	data := []byte("<svg xmlns=\"http://www.w3.org/2000/svg\"><!--" + strings.Repeat("x", 1024*1024) + "--></svg>")
	_, err := Compress(data, "diagram.svg", DefaultConfig())
	if err == nil || !IsPermanent(err) {
		t.Fatalf("error = %v, want permanent SVG size error", err)
	}
}

func TestCompressRejectsActiveSVG(t *testing.T) {
	t.Parallel()
	data := []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`)
	_, err := Compress(data, "diagram.svg", DefaultConfig())
	if err == nil || !IsPermanent(err) || !strings.Contains(err.Error(), "active") {
		t.Fatalf("error = %v, want permanent active-SVG error", err)
	}
}

func TestCompressRejectsCorruptImage(t *testing.T) {
	t.Parallel()
	cfg := DefaultConfig()
	cfg.MinBytes = 0
	_, err := Compress([]byte("not an image"), "broken.png", cfg)
	if err == nil || !IsPermanent(err) {
		t.Fatalf("error = %v, want permanent error", err)
	}
}

func TestCompressDelegatesOversizedStaticImageToSafeDownsampler(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MinBytes = 0
	called := false
	cfg.oversizedStaticProcessor = func(data []byte, fileName string, processorCfg Config) ([]byte, error) {
		called = true
		if fileName != "huge.png" || processorCfg.MaxWidth != 1920 || processorCfg.MaxHeight != 1920 {
			t.Fatalf("unexpected downsampler arguments: file=%q cfg=%+v", fileName, processorCfg)
		}
		var output bytes.Buffer
		if err := genwebp.Encode(&output, image.NewNRGBA(image.Rect(0, 0, 1920, 1440)), genwebp.Options{Quality: 75}); err != nil {
			t.Fatal(err)
		}
		return output.Bytes(), nil
	}

	input := append(pngWithDimensions(8000, 6000), bytes.Repeat([]byte{0}, cfg.TargetBytes)...)
	got, err := Compress(input, "huge.png", cfg)
	if err != nil {
		t.Fatalf("Compress() error = %v", err)
	}
	if !called || got.Format != "webp" || got.Width != 1920 || got.Height != 1440 || !got.Compressed {
		t.Fatalf("oversized image was not safely downsampled: called=%v result=%+v", called, got)
	}
}

func TestCompressPreservesRetryableOversizedProcessorFailure(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MinBytes = 0
	cfg.oversizedStaticProcessor = func([]byte, string, Config) ([]byte, error) {
		return nil, &RetryableError{Reason: "processor temporarily busy"}
	}

	input := append(pngWithDimensions(8000, 6000), bytes.Repeat([]byte{0}, cfg.TargetBytes)...)
	_, err := Compress(input, "huge.png", cfg)
	if err == nil || !IsRetryable(err) || IsPermanent(err) {
		t.Fatalf("error = %v, want retryable processor error", err)
	}
}

func TestCanUseOversizedStaticProcessorDoesNotFlattenAnimatedWebP(t *testing.T) {
	animated := []byte("RIFF\x10\x00\x00\x00WEBPANIM\x00\x00\x00\x00")
	if canUseOversizedStaticProcessor("webp", animated) {
		t.Fatal("animated WebP must not use the static image processor")
	}
}

func TestCompressOversizedStaticInvokesVipsThumbnailSafely(t *testing.T) {
	output := filepath.Join(t.TempDir(), "expected.webp")
	var encoded bytes.Buffer
	if err := genwebp.Encode(&encoded, image.NewNRGBA(image.Rect(0, 0, 1920, 1200)), genwebp.Options{Quality: 75}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(output, encoded.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	trace := filepath.Join(t.TempDir(), "vips-arguments")
	binary := filepath.Join(t.TempDir(), "vips")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$WEKNORA_VIPS_TEST_TRACE\"\nout=${3%%[*}\ncp \"$WEKNORA_VIPS_TEST_OUTPUT\" \"$out\"\n"
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WEKNORA_VIPS_BINARY", binary)
	t.Setenv("WEKNORA_VIPS_TEST_TRACE", trace)
	t.Setenv("WEKNORA_VIPS_TEST_OUTPUT", output)

	cfg := DefaultConfig()
	got, err := compressOversizedStatic([]byte("not decoded by the fake processor"), "camera.jpg", cfg)
	if err != nil {
		t.Fatalf("compressOversizedStatic() error = %v", err)
	}
	if !bytes.Equal(got, encoded.Bytes()) {
		t.Fatal("processor output was not returned")
	}
	arguments, err := os.ReadFile(trace)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"thumbnail", "--height", "1920", "--size", "down", "[Q=75,effort=4]"} {
		if !strings.Contains(string(arguments), expected) {
			t.Fatalf("vips arguments missing %q: %s", expected, arguments)
		}
	}
}

func noisyNRGBA(width, height int, transparentOrigin bool) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, width, height))
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i] = uint8(rand.Uint32())
		img.Pix[i+1] = uint8(rand.Uint32())
		img.Pix[i+2] = uint8(rand.Uint32())
		img.Pix[i+3] = 255
	}
	if transparentOrigin {
		img.SetNRGBA(0, 0, color.NRGBA{})
	}
	return img
}

func pngWithDimensions(width, height uint32) []byte {
	data := []byte{137, 80, 78, 71, 13, 10, 26, 10, 0, 0, 0, 13, 'I', 'H', 'D', 'R'}
	ihdr := []byte{
		byte(width >> 24), byte(width >> 16), byte(width >> 8), byte(width),
		byte(height >> 24), byte(height >> 16), byte(height >> 8), byte(height),
		8, 6, 0, 0, 0,
	}
	data = append(data, ihdr...)
	checksumInput := append([]byte("IHDR"), ihdr...)
	var checksum [4]byte
	binary.BigEndian.PutUint32(checksum[:], crc32.ChecksumIEEE(checksumInput))
	data = append(data, checksum[:]...)
	return data
}
