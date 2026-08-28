// Package imagecompression provides bounded, adaptive image compression for
// permanent object-storage images.
package imagecompression

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"image"
	"image/color/palette"
	"image/draw"
	"image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"path/filepath"
	"strings"

	"github.com/disintegration/imaging"
	genwebp "github.com/gen2brain/webp"
	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/tiff"
)

const AlgorithmVersion = "adaptive-webp-v1"

type Config struct {
	MinBytes           int
	TargetBytes        int
	MaxPixels          int64
	MaxAnimationPixels int64
	MaxWidth           int
	MaxHeight          int
	InitialQuality     int
	MinQuality         int
}

func DefaultConfig() Config {
	return Config{
		MinBytes:           100 * 1024,
		TargetBytes:        1024 * 1024,
		MaxPixels:          40_000_000,
		MaxAnimationPixels: 120_000_000,
		MaxWidth:           1920,
		MaxHeight:          1920,
		InitialQuality:     75,
		MinQuality:         60,
	}
}

type Result struct {
	Data             []byte
	FileName         string
	Format           string
	ContentType      string
	Compressed       bool
	OriginalSize     int64
	CompressedSize   int64
	CompressionRatio float64
	Width            int
	Height           int
	OriginalHash     string
	OriginalFormat   string
	StoredHash       string
	AlgorithmVersion string
}

type PermanentError struct {
	Reason string
	Err    error
}

func (e *PermanentError) Error() string {
	if e.Err == nil {
		return e.Reason
	}
	return e.Reason + ": " + e.Err.Error()
}

func (e *PermanentError) Unwrap() error { return e.Err }

func IsPermanent(err error) bool {
	var target *PermanentError
	return errors.As(err, &target)
}

func Compress(data []byte, fileName string, cfg Config) (Result, error) {
	cfg = normalizeConfig(cfg)
	base := Result{
		Data:             data,
		FileName:         fileName,
		OriginalSize:     int64(len(data)),
		CompressedSize:   int64(len(data)),
		OriginalHash:     digest(data),
		StoredHash:       digest(data),
		AlgorithmVersion: AlgorithmVersion,
	}

	if looksLikeSVG(data, fileName) {
		base.Format, base.ContentType = "svg", "image/svg+xml"
		base.OriginalFormat = "svg"
		base.FileName = replaceExtension(fileName, "svg")
		if len(data) > cfg.TargetBytes {
			return Result{}, &PermanentError{Reason: "SVG exceeds the permanent image size limit"}
		}
		if err := validateSVG(data); err != nil {
			return Result{}, &PermanentError{Reason: "SVG validation failed", Err: err}
		}
		return base, nil
	}

	decodedCfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return Result{}, &PermanentError{Reason: "image decode configuration failed", Err: err}
	}
	base.Format = normalizeFormat(format)
	base.OriginalFormat = base.Format
	base.ContentType = contentType(base.Format)
	base.FileName = replaceExtension(fileName, base.Format)
	base.Width, base.Height = decodedCfg.Width, decodedCfg.Height
	if decodedCfg.Width <= 0 || decodedCfg.Height <= 0 ||
		int64(decodedCfg.Width)*int64(decodedCfg.Height) > cfg.MaxPixels {
		return Result{}, &PermanentError{Reason: fmt.Sprintf("image pixel count exceeds limit (%d)", cfg.MaxPixels)}
	}
	if len(data) < cfg.MinBytes {
		return base, nil
	}

	var output []byte
	var outputFormat string
	switch base.Format {
	case "gif":
		output, err = compressGIF(data, cfg)
		outputFormat = "gif"
	case "webp":
		output, outputFormat, err = compressWebP(data, cfg)
	default:
		output, err = compressStatic(data, cfg)
		outputFormat = "webp"
	}
	if err != nil {
		if IsPermanent(err) {
			return Result{}, err
		}
		return Result{}, &PermanentError{Reason: "image compression failed", Err: err}
	}
	if len(output) == 0 {
		return Result{}, &PermanentError{Reason: "image compression produced empty output"}
	}

	if len(data) <= cfg.TargetBytes && len(output) >= len(data) {
		return base, nil
	}
	if len(output) > cfg.TargetBytes {
		return Result{}, &PermanentError{Reason: fmt.Sprintf("image cannot be compressed below %d bytes without crossing the quality floor", cfg.TargetBytes)}
	}

	storedCfg, _, err := image.DecodeConfig(bytes.NewReader(output))
	if err != nil {
		return Result{}, &PermanentError{Reason: "compressed image verification failed", Err: err}
	}
	result := Result{
		Data:             output,
		FileName:         replaceExtension(fileName, outputFormat),
		Format:           outputFormat,
		ContentType:      contentType(outputFormat),
		Compressed:       true,
		OriginalSize:     int64(len(data)),
		CompressedSize:   int64(len(output)),
		Width:            storedCfg.Width,
		Height:           storedCfg.Height,
		OriginalHash:     base.OriginalHash,
		OriginalFormat:   base.OriginalFormat,
		StoredHash:       digest(output),
		AlgorithmVersion: AlgorithmVersion,
	}
	result.CompressionRatio = float64(result.OriginalSize-result.CompressedSize) / float64(result.OriginalSize)
	return result, nil
}

func compressStatic(data []byte, cfg Config) ([]byte, error) {
	img, err := imaging.Decode(bytes.NewReader(data), imaging.AutoOrientation(true))
	if err != nil {
		return nil, err
	}
	return encodeStaticSteps(img, cfg)
}

func encodeStaticSteps(img image.Image, cfg Config) ([]byte, error) {
	var smallest []byte
	for _, maxSide := range dimensionSteps(cfg) {
		resized := fit(img, maxSide, maxSide)
		for quality := cfg.InitialQuality; quality >= cfg.MinQuality; quality -= 5 {
			var buf bytes.Buffer
			if err := genwebp.Encode(&buf, resized, genwebp.Options{Quality: quality, Method: 4, Exact: true}); err != nil {
				return nil, err
			}
			candidate := append([]byte(nil), buf.Bytes()...)
			if len(smallest) == 0 || len(candidate) < len(smallest) {
				smallest = candidate
			}
			if len(candidate) <= cfg.TargetBytes {
				return candidate, nil
			}
		}
	}
	return smallest, nil
}

func compressGIF(data []byte, cfg Config) ([]byte, error) {
	animation, err := gif.DecodeAll(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	if len(animation.Image) <= 1 {
		// Keep the GIF container even for a single-frame input. The caller uses
		// the returned container type to choose the stored extension/content
		// type, so returning WebP bytes here would create a mislabeled object.
		// The normal GIF path below is also cheap for one frame.
	}
	framePixels := int64(animation.Config.Width) * int64(animation.Config.Height) * int64(len(animation.Image))
	if framePixels > cfg.MaxAnimationPixels {
		return nil, &PermanentError{Reason: fmt.Sprintf("animation pixel count exceeds limit (%d)", cfg.MaxAnimationPixels)}
	}
	var smallest []byte
	for _, maxSide := range dimensionSteps(cfg) {
		out := &gif.GIF{
			Delay:           append([]int(nil), animation.Delay...),
			LoopCount:       animation.LoopCount,
			Disposal:        append([]byte(nil), animation.Disposal...),
			BackgroundIndex: animation.BackgroundIndex,
		}
		for _, frame := range animation.Image {
			resized := fit(frame, maxSide, maxSide)
			paletted := image.NewPaletted(resized.Bounds(), palette.Plan9)
			draw.FloydSteinberg.Draw(paletted, paletted.Rect, resized, resized.Bounds().Min)
			out.Image = append(out.Image, paletted)
		}
		out.Config.Width = out.Image[0].Bounds().Dx()
		out.Config.Height = out.Image[0].Bounds().Dy()
		var buf bytes.Buffer
		if err := gif.EncodeAll(&buf, out); err != nil {
			return nil, err
		}
		candidate := append([]byte(nil), buf.Bytes()...)
		if len(smallest) == 0 || len(candidate) < len(smallest) {
			smallest = candidate
		}
		if len(candidate) <= cfg.TargetBytes {
			return candidate, nil
		}
	}
	return smallest, nil
}

func compressWebP(data []byte, cfg Config) ([]byte, string, error) {
	animation, err := genwebp.DecodeAll(bytes.NewReader(data), genwebp.Options{AutoRotate: true})
	if err != nil {
		return nil, "", err
	}
	if len(animation.Image) <= 1 {
		output, err := encodeStaticSteps(animation.Image[0], cfg)
		return output, "webp", err
	}
	framePixels := int64(animation.Image[0].Bounds().Dx()) * int64(animation.Image[0].Bounds().Dy()) * int64(len(animation.Image))
	if framePixels > cfg.MaxAnimationPixels {
		return nil, "", &PermanentError{Reason: fmt.Sprintf("animation pixel count exceeds limit (%d)", cfg.MaxAnimationPixels)}
	}
	var smallest []byte
	for _, maxSide := range dimensionSteps(cfg) {
		for quality := cfg.InitialQuality; quality >= cfg.MinQuality; quality -= 5 {
			out := &genwebp.WEBP{Delay: append([]int(nil), animation.Delay...), LoopCount: animation.LoopCount}
			for _, frame := range animation.Image {
				out.Image = append(out.Image, fit(frame, maxSide, maxSide))
			}
			var buf bytes.Buffer
			if err := genwebp.EncodeAll(&buf, out, genwebp.Options{Quality: quality, Method: 4, Exact: true}); err != nil {
				return nil, "", err
			}
			candidate := append([]byte(nil), buf.Bytes()...)
			if len(smallest) == 0 || len(candidate) < len(smallest) {
				smallest = candidate
			}
			if len(candidate) <= cfg.TargetBytes {
				return candidate, "webp", nil
			}
		}
	}
	return smallest, "webp", nil
}

func fit(img image.Image, maxWidth, maxHeight int) image.Image {
	bounds := img.Bounds()
	if bounds.Dx() <= maxWidth && bounds.Dy() <= maxHeight {
		return imaging.Clone(img)
	}
	return imaging.Fit(img, maxWidth, maxHeight, imaging.Lanczos)
}

func dimensionSteps(cfg Config) []int {
	maximum := cfg.MaxWidth
	if cfg.MaxHeight < maximum {
		maximum = cfg.MaxHeight
	}
	candidates := []int{maximum, 1600, 1280, 1024, 960}
	seen := make(map[int]bool, len(candidates))
	steps := make([]int, 0, len(candidates))
	for _, value := range candidates {
		if value <= 0 || value > maximum || seen[value] {
			continue
		}
		seen[value] = true
		steps = append(steps, value)
	}
	return steps
}

func normalizeConfig(cfg Config) Config {
	defaults := DefaultConfig()
	if cfg.MinBytes < 0 {
		cfg.MinBytes = 0
	}
	if cfg.TargetBytes <= 0 {
		cfg.TargetBytes = defaults.TargetBytes
	}
	if cfg.MaxPixels <= 0 {
		cfg.MaxPixels = defaults.MaxPixels
	}
	if cfg.MaxAnimationPixels <= 0 {
		cfg.MaxAnimationPixels = defaults.MaxAnimationPixels
	}
	if cfg.MaxWidth <= 0 {
		cfg.MaxWidth = defaults.MaxWidth
	}
	if cfg.MaxHeight <= 0 {
		cfg.MaxHeight = defaults.MaxHeight
	}
	if cfg.InitialQuality <= 0 || cfg.InitialQuality > 100 {
		cfg.InitialQuality = defaults.InitialQuality
	}
	if cfg.MinQuality <= 0 || cfg.MinQuality > cfg.InitialQuality {
		cfg.MinQuality = defaults.MinQuality
	}
	return cfg
}

func normalizeFormat(format string) string {
	format = strings.ToLower(format)
	if format == "jpeg" {
		return "jpg"
	}
	return format
}

func contentType(format string) string {
	switch format {
	case "jpg", "jpeg":
		return "image/jpeg"
	case "png":
		return "image/png"
	case "gif":
		return "image/gif"
	case "webp":
		return "image/webp"
	case "bmp":
		return "image/bmp"
	case "tiff", "tif":
		return "image/tiff"
	case "svg":
		return "image/svg+xml"
	default:
		return "application/octet-stream"
	}
}

func replaceExtension(fileName, format string) string {
	extension := "." + format
	if format == "jpg" {
		extension = ".jpg"
	}
	base := strings.TrimSuffix(fileName, filepath.Ext(fileName))
	if base == "" {
		base = "image"
	}
	return base + extension
}

func digest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func looksLikeSVG(data []byte, fileName string) bool {
	if strings.EqualFold(filepath.Ext(fileName), ".svg") {
		return true
	}
	prefix := strings.ToLower(strings.TrimSpace(string(data[:min(len(data), 512)])))
	return strings.HasPrefix(prefix, "<svg") || (strings.HasPrefix(prefix, "<?xml") && strings.Contains(prefix, "<svg"))
}

func validateSVG(data []byte) error {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	decoder.Strict = true
	rootSeen := false
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		start, ok := token.(xml.StartElement)
		if !ok {
			continue
		}
		name := strings.ToLower(start.Name.Local)
		if !rootSeen {
			if name != "svg" {
				return fmt.Errorf("root element is %q, want svg", start.Name.Local)
			}
			rootSeen = true
		}
		switch name {
		case "script", "foreignobject", "iframe", "object", "embed":
			return fmt.Errorf("active element %q is not allowed", start.Name.Local)
		}
		for _, attr := range start.Attr {
			attrName := strings.ToLower(attr.Name.Local)
			attrValue := strings.ToLower(strings.TrimSpace(attr.Value))
			if strings.HasPrefix(attrName, "on") ||
				((attrName == "href" || attrName == "src") && strings.HasPrefix(attrValue, "javascript:")) {
				return fmt.Errorf("active attribute %q is not allowed", attr.Name.Local)
			}
		}
	}
	if !rootSeen {
		return errors.New("missing svg root element")
	}
	return nil
}
