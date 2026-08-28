package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/hibiken/asynq"
	"github.com/stretchr/testify/require"
)

type imageCompressionRepoStub struct {
	interfaces.KnowledgeRepository
	items  map[string]*types.Knowledge
	events *[]string
}

func (r *imageCompressionRepoStub) ListKnowledgeByKnowledgeBaseID(_ context.Context, _ uint64, kbID string) ([]*types.Knowledge, error) {
	items := make([]*types.Knowledge, 0, len(r.items))
	for _, item := range r.items {
		if item.KnowledgeBaseID == kbID {
			copyItem := *item
			items = append(items, &copyItem)
		}
	}
	return items, nil
}

func (r *imageCompressionRepoStub) GetKnowledgeByID(_ context.Context, _ uint64, id string) (*types.Knowledge, error) {
	item, ok := r.items[id]
	if !ok {
		return nil, errors.New("not found")
	}
	copyItem := *item
	return &copyItem, nil
}

func (r *imageCompressionRepoStub) UpdateKnowledgeColumns(_ context.Context, id string, values map[string]interface{}) error {
	if r.events != nil {
		*r.events = append(*r.events, "db-switch")
	}
	item := r.items[id]
	if value, ok := values["file_path"].(string); ok {
		item.FilePath = value
	}
	if value, ok := values["file_name"].(string); ok {
		item.FileName = value
	}
	if value, ok := values["file_type"].(string); ok {
		item.FileType = value
	}
	if value, ok := values["file_size"].(int64); ok {
		item.FileSize = value
	}
	if value, ok := values["compression_info"].(types.JSON); ok {
		item.CompressionInfo = append(types.JSON(nil), value...)
	}
	return nil
}

type imageCompressionFileStub struct {
	files     map[string][]byte
	readErr   map[string]error
	events    *[]string
	savedData []byte
	deleted   []string
}

func (f *imageCompressionFileStub) CheckConnectivity(context.Context) error { return nil }
func (f *imageCompressionFileStub) SaveFile(context.Context, *multipart.FileHeader, uint64, string) (string, error) {
	return "", errors.New("not implemented")
}
func (f *imageCompressionFileStub) SaveBytes(_ context.Context, data []byte, _ uint64, _ string, _ bool) (string, error) {
	f.savedData = append([]byte(nil), data...)
	f.files["stored/new.webp"] = append([]byte(nil), data...)
	return "stored/new.webp", nil
}
func (f *imageCompressionFileStub) GetFile(_ context.Context, path string) (io.ReadCloser, error) {
	if err := f.readErr[path]; err != nil {
		return nil, err
	}
	return io.NopCloser(bytes.NewReader(f.files[path])), nil
}
func (f *imageCompressionFileStub) GetFileURL(context.Context, string) (string, error) {
	return "", errors.New("not implemented")
}
func (f *imageCompressionFileStub) DeleteFile(_ context.Context, path string) error {
	if f.events != nil {
		*f.events = append(*f.events, "delete:"+path)
	}
	f.deleted = append(f.deleted, path)
	delete(f.files, path)
	return nil
}
func (f *imageCompressionFileStub) CopyFile(context.Context, string, uint64, string) (string, error) {
	return "", errors.New("not implemented")
}

type imageCompressionTaskStub struct {
	payloads []types.KnowledgeImageCompressionPayload
}

func (s *imageCompressionTaskStub) Enqueue(task *asynq.Task, _ ...asynq.Option) (*asynq.TaskInfo, error) {
	var payload types.KnowledgeImageCompressionPayload
	if err := json.Unmarshal(task.Payload(), &payload); err != nil {
		return nil, err
	}
	s.payloads = append(s.payloads, payload)
	return &asynq.TaskInfo{ID: "queued", Queue: types.QueueLow}, nil
}

func TestCompressExistingKnowledgeImageSwitchesDBBeforeDeletingOriginal(t *testing.T) {
	original := noisyPNG(t, 700, 700)
	events := make([]string, 0)
	item := &types.Knowledge{
		ID: "img-1", TenantID: 1, KnowledgeBaseID: "kb-1", ParseStatus: types.ParseStatusCompleted,
		FileName: "scan.png", FileType: "png", FileSize: int64(len(original)), FileHash: "original-hash", FilePath: "stored/old.png",
	}
	repo := &imageCompressionRepoStub{items: map[string]*types.Knowledge{"img-1": item}, events: &events}
	fileSvc := &imageCompressionFileStub{files: map[string][]byte{"stored/old.png": original}, readErr: map[string]error{}, events: &events}
	svc := &knowledgeService{
		repo: repo, fileSvc: fileSvc,
		kbService: &createKnowledgeFileKBServiceStub{kb: &types.KnowledgeBase{ID: "kb-1"}},
	}

	before, after, skipped, err := svc.compressExistingKnowledgeImage(newCreateKnowledgeFileContext(), "kb-1", "img-1")

	require.NoError(t, err)
	require.False(t, skipped)
	require.Equal(t, int64(len(original)), before)
	require.LessOrEqual(t, after, int64(1024*1024))
	require.Equal(t, "original-hash", item.FileHash)
	require.Equal(t, "stored/new.webp", item.FilePath)
	require.Equal(t, []string{"db-switch", "delete:stored/old.png", "db-switch"}, events)
}

func TestProcessKnowledgeImageCompressionRetriesTransientItemsAfterWholeRound(t *testing.T) {
	items := map[string]*types.Knowledge{
		"bad":   {ID: "bad", TenantID: 1, KnowledgeBaseID: "kb-1", ParseStatus: types.ParseStatusCompleted, FileName: "bad.png", FileType: "png", FileSize: 2 << 20, FilePath: "stored/bad.png"},
		"later": {ID: "later", TenantID: 1, KnowledgeBaseID: "kb-1", ParseStatus: types.ParseStatusCompleted, FileName: "later.png", FileType: "png", FileSize: 2 << 20, FilePath: "stored/later.png"},
	}
	taskQueue := &imageCompressionTaskStub{}
	svc := &knowledgeService{
		repo: &imageCompressionRepoStub{items: items}, task: taskQueue,
		fileSvc: &imageCompressionFileStub{
			files:   map[string][]byte{"stored/bad.png": []byte("not an image")},
			readErr: map[string]error{"stored/later.png": errors.New("temporary storage outage")},
		},
		kbService: &createKnowledgeFileKBServiceStub{kb: &types.KnowledgeBase{ID: "kb-1"}},
	}
	now := time.Now().Unix()
	require.NoError(t, svc.saveKnowledgeImageCompressionProgress(context.Background(), &types.KnowledgeImageCompressionProgress{
		TaskID: "task-1", KBID: "kb-1", Total: 2, CreatedAt: now, UpdatedAt: now,
	}))
	payload, err := json.Marshal(types.KnowledgeImageCompressionPayload{
		TenantID: 1, TaskID: "task-1", KBID: "kb-1", KnowledgeIDs: []string{"bad", "later"},
	})
	require.NoError(t, err)

	require.NoError(t, svc.ProcessKnowledgeImageCompression(context.Background(), asynq.NewTask(types.TypeKnowledgeImageCompress, payload)))
	require.Len(t, taskQueue.payloads, 1)
	require.Equal(t, []string{"later"}, taskQueue.payloads[0].KnowledgeIDs)
	require.Equal(t, 1, taskQueue.payloads[0].RetryRound)
	progress, err := svc.GetKnowledgeImageCompressionProgress(context.Background(), "task-1")
	require.NoError(t, err)
	require.Equal(t, 1, progress.PermanentFailed)
	require.Equal(t, 1, progress.Retrying)
}

func TestKnowledgeImageCompressionLockPreventsConcurrentRuns(t *testing.T) {
	svc := &knowledgeService{}
	ctx := context.Background()
	first, err := svc.acquireKnowledgeImageCompressionLock(ctx, "kb-1", "task-1")
	require.NoError(t, err)
	require.True(t, first)
	second, err := svc.acquireKnowledgeImageCompressionLock(ctx, "kb-1", "task-2")
	require.NoError(t, err)
	require.False(t, second)
	svc.releaseKnowledgeImageCompressionLock(ctx, "kb-1", "task-1")
	third, err := svc.acquireKnowledgeImageCompressionLock(ctx, "kb-1", "task-3")
	require.NoError(t, err)
	require.True(t, third)
}

func TestStartKnowledgeImageCompressionSplitsCandidatesIntoHundredImageBatches(t *testing.T) {
	items := make(map[string]*types.Knowledge, 101)
	for i := 0; i < 101; i++ {
		id := fmt.Sprintf("img-%03d", i)
		items[id] = &types.Knowledge{
			ID: id, TenantID: 1, KnowledgeBaseID: "kb-1", ParseStatus: types.ParseStatusCompleted,
			FileType: "png", FilePath: "stored/" + id, FileSize: 2 << 20,
		}
	}
	taskQueue := &imageCompressionTaskStub{}
	svc := &knowledgeService{repo: &imageCompressionRepoStub{items: items}, task: taskQueue}

	taskID, err := svc.StartKnowledgeImageCompression(newCreateKnowledgeFileContext(), "kb-1")

	require.NoError(t, err)
	require.NotEmpty(t, taskID)
	require.Len(t, taskQueue.payloads, 2)
	require.Len(t, taskQueue.payloads[0].KnowledgeIDs, 100)
	require.Len(t, taskQueue.payloads[1].KnowledgeIDs, 1)
	progress, err := svc.GetKnowledgeImageCompressionProgress(context.Background(), taskID)
	require.NoError(t, err)
	require.Equal(t, 2, progress.BatchesRemaining)
}

func TestImageCompressionCandidatesIncludesPendingCleanup(t *testing.T) {
	cleanupInfo, err := json.Marshal(map[string]string{
		"status":        "cleanup_pending",
		"original_path": "stored/old.png",
	})
	require.NoError(t, err)
	svc := &knowledgeService{repo: &imageCompressionRepoStub{items: map[string]*types.Knowledge{
		"cleanup": {
			ID: "cleanup", TenantID: 1, KnowledgeBaseID: "kb-1", ParseStatus: types.ParseStatusCompleted,
			FileType: "webp", FilePath: "stored/new.webp", FileSize: 512 * 1024, CompressionInfo: cleanupInfo,
		},
		"small": {
			ID: "small", TenantID: 1, KnowledgeBaseID: "kb-1", ParseStatus: types.ParseStatusCompleted,
			FileType: "png", FilePath: "stored/small.png", FileSize: 512 * 1024,
		},
	}}}

	candidates, err := svc.imageCompressionCandidates(newCreateKnowledgeFileContext(), "kb-1")

	require.NoError(t, err)
	require.Len(t, candidates, 1)
	require.Equal(t, "cleanup", candidates[0].ID)
}
