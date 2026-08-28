package session

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"image"
	"image/png"
	"io"
	"math/rand/v2"
	"mime/multipart"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

type chatImageFileStub struct {
	data     []byte
	fileName string
}

func (*chatImageFileStub) CheckConnectivity(context.Context) error { return nil }
func (*chatImageFileStub) SaveFile(context.Context, *multipart.FileHeader, uint64, string) (string, error) {
	return "", errors.New("not implemented")
}
func (s *chatImageFileStub) SaveBytes(_ context.Context, data []byte, _ uint64, fileName string, _ bool) (string, error) {
	s.data = append([]byte(nil), data...)
	s.fileName = fileName
	return "stored/" + fileName, nil
}
func (*chatImageFileStub) GetFile(context.Context, string) (io.ReadCloser, error) {
	return nil, errors.New("not implemented")
}
func (*chatImageFileStub) GetFileURL(context.Context, string) (string, error) {
	return "", errors.New("not implemented")
}
func (*chatImageFileStub) DeleteFile(context.Context, string) error { return nil }
func (*chatImageFileStub) CopyFile(context.Context, string, uint64, string) (string, error) {
	return "", errors.New("not implemented")
}

func TestSaveImageAttachmentsStoresCompressedCopyAndKeepsOriginalForVLM(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 700, 700))
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i] = uint8(rand.Uint32())
		img.Pix[i+1] = uint8(rand.Uint32())
		img.Pix[i+2] = uint8(rand.Uint32())
		img.Pix[i+3] = 255
	}
	var original bytes.Buffer
	require.NoError(t, png.Encode(&original, img))
	dataURI := "data:image/png;base64," + base64.StdEncoding.EncodeToString(original.Bytes())
	attachments := []ImageAttachment{{Data: dataURI}}
	fileSvc := &chatImageFileStub{}
	handler := &Handler{fileService: fileSvc}

	require.NoError(t, handler.saveImageAttachments(context.Background(), attachments, 1, ""))
	require.Equal(t, dataURI, attachments[0].Data, "original data URI must remain available to VLM analysis")
	require.LessOrEqual(t, len(fileSvc.data), 1024*1024)
	require.True(t, strings.HasSuffix(fileSvc.fileName, ".webp"))
	require.NotEmpty(t, attachments[0].URL)
}

func TestMimeToExtSupportsAllAcceptedRasterAndSVGFormats(t *testing.T) {
	require.Equal(t, ".bmp", mimeToExt("image/bmp"))
	require.Equal(t, ".tiff", mimeToExt("image/tiff"))
	require.Equal(t, ".svg", mimeToExt("image/svg+xml"))
}
