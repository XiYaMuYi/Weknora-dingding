package imagecompression

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const oversizedStaticTimeout = 90 * time.Second

type oversizedStaticProcessor func(data []byte, fileName string, cfg Config) ([]byte, error)

// One libvips process can use substantial native memory while decoding a large
// photograph. Serializing this exceptional path keeps it isolated from normal
// image uploads and from the standard in-process compressor.
var oversizedStaticSemaphore = make(chan struct{}, 1)

func canUseOversizedStaticProcessor(format string, data []byte) bool {
	switch format {
	case "jpg", "png", "bmp", "tiff":
		return true
	case "webp":
		return !isAnimatedWebP(data)
	default:
		// Animated GIFs are intentionally kept on their existing path so their
		// frames, delays, and loop behaviour cannot be flattened by a thumbnailer.
		return false
	}
}

func compressOversizedStatic(data []byte, fileName string, cfg Config) ([]byte, error) {
	select {
	case oversizedStaticSemaphore <- struct{}{}:
		defer func() { <-oversizedStaticSemaphore }()
	case <-time.After(oversizedStaticTimeout):
		return nil, &RetryableError{Reason: "timed out waiting for the oversized image processor"}
	}

	workDir, err := os.MkdirTemp("", "weknora-large-image-*")
	if err != nil {
		return nil, &RetryableError{Reason: "create oversized image workspace", Err: err}
	}
	defer os.RemoveAll(workDir)

	ext := strings.ToLower(filepath.Ext(fileName))
	if ext == "" || len(ext) > 10 {
		ext = ".img"
	}
	sourcePath := filepath.Join(workDir, "source"+ext)
	if err := os.WriteFile(sourcePath, data, 0o600); err != nil {
		return nil, &RetryableError{Reason: "write oversized image source", Err: err}
	}

	ctx, cancel := context.WithTimeout(context.Background(), oversizedStaticTimeout)
	defer cancel()
	var smallest []byte
	for _, maxSide := range dimensionSteps(cfg) {
		for quality := cfg.InitialQuality; quality >= cfg.MinQuality; quality -= 5 {
			outputPath := filepath.Join(workDir, fmt.Sprintf("%d-%d.webp", maxSide, quality))
			if err := runVipsThumbnail(ctx, sourcePath, outputPath, maxSide, quality); err != nil {
				return nil, err
			}
			candidate, err := os.ReadFile(outputPath)
			if err != nil {
				return nil, &RetryableError{Reason: "read oversized image output", Err: err}
			}
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

func runVipsThumbnail(ctx context.Context, sourcePath, outputPath string, maxSide, quality int) error {
	binaryName := os.Getenv("WEKNORA_VIPS_BINARY")
	if binaryName == "" {
		binaryName = "vips"
	}
	outputSpec := outputPath + "[Q=" + strconv.Itoa(quality) + ",effort=4]"
	cmd := exec.CommandContext(ctx, binaryName, "thumbnail", sourcePath, outputSpec, strconv.Itoa(maxSide), "--height", strconv.Itoa(maxSide), "--size", "down")
	cmd.Env = append(os.Environ(), "VIPS_CONCURRENCY=1")
	commandOutput, err := cmd.CombinedOutput()
	if err == nil {
		return nil
	}
	if errors.Is(err, exec.ErrNotFound) {
		return &PermanentError{Reason: "oversized image processor is unavailable", Err: err}
	}
	if ctx.Err() != nil {
		return &RetryableError{Reason: "oversized image processor timed out", Err: ctx.Err()}
	}
	message := strings.TrimSpace(string(commandOutput))
	if len(message) > 512 {
		message = message[:512]
	}
	return &RetryableError{Reason: "oversized image processor failed", Err: fmt.Errorf("%w: %s", err, message)}
}

func isAnimatedWebP(data []byte) bool {
	if len(data) < 12 || string(data[:4]) != "RIFF" || string(data[8:12]) != "WEBP" {
		return false
	}
	for offset := 12; offset+8 <= len(data); {
		chunkName := string(data[offset : offset+4])
		chunkSize := int(binary.LittleEndian.Uint32(data[offset+4 : offset+8]))
		if chunkName == "ANIM" || chunkName == "ANMF" {
			return true
		}
		next := offset + 8 + chunkSize
		if chunkSize%2 == 1 {
			next++
		}
		if next <= offset || next > len(data) {
			return false
		}
		offset = next
	}
	return false
}
