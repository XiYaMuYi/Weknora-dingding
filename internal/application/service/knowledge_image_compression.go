package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	werrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/imagecompression"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/google/uuid"
	"github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"
)

const (
	knowledgeImageCompressionProgressPrefix = "knowledge_image_compression_progress:"
	knowledgeImageCompressionLockPrefix     = "knowledge_image_compression_lock:"
	knowledgeImageCompressionProgressTTL    = 7 * 24 * time.Hour
	knowledgeImageCompressionMaxRetryRounds = 3
	knowledgeImageCompressionReadLimit      = 100 * 1024 * 1024
	knowledgeImageCompressionBatchSize      = 100
)

var knowledgeImageCompressionRetryDelay = [...]time.Duration{
	30 * time.Second,
	2 * time.Minute,
	10 * time.Minute,
}

func (s *knowledgeService) imageCompressionCandidates(ctx context.Context, kbID string) ([]*types.Knowledge, error) {
	tenantID := types.MustTenantIDFromContext(ctx)
	items, err := s.repo.ListKnowledgeByKnowledgeBaseID(ctx, tenantID, kbID)
	if err != nil {
		return nil, err
	}
	result := make([]*types.Knowledge, 0)
	for _, item := range items {
		if item.ParseStatus != types.ParseStatusCompleted {
			continue
		}
		if !IsImageType(item.FileType) || item.FilePath == "" {
			continue
		}
		// A previous attempt may have switched the database row successfully but
		// failed while deleting the superseded object. Keep retrying that cleanup
		// even though the replacement itself is already below the target size.
		if item.FileSize <= int64(imagecompression.DefaultConfig().TargetBytes) && compressionCleanupPath(item.CompressionInfo) == "" {
			continue
		}
		result = append(result, item)
	}
	return result, nil
}

func (s *knowledgeService) PreviewKnowledgeImageCompression(ctx context.Context, kbID string) (*types.KnowledgeImageCompressionPreview, error) {
	items, err := s.imageCompressionCandidates(ctx, kbID)
	if err != nil {
		return nil, err
	}
	preview := &types.KnowledgeImageCompressionPreview{
		KBID:           kbID,
		EligibleImages: len(items),
		TargetBytes:    int64(imagecompression.DefaultConfig().TargetBytes),
	}
	for _, item := range items {
		preview.TotalSourceBytes += item.FileSize
	}
	return preview, nil
}

func (s *knowledgeService) StartKnowledgeImageCompression(ctx context.Context, kbID string) (string, error) {
	taskID := uuid.NewString()
	acquired, err := s.acquireKnowledgeImageCompressionLock(ctx, kbID, taskID)
	if err != nil {
		return "", err
	}
	if !acquired {
		return "", werrors.NewConflictError("An image compression task is already running for this knowledge base")
	}
	items, err := s.imageCompressionCandidates(ctx, kbID)
	if err != nil {
		s.releaseKnowledgeImageCompressionLock(ctx, kbID, taskID)
		return "", err
	}
	now := time.Now().Unix()
	progress := &types.KnowledgeImageCompressionProgress{
		TaskID: taskID, KBID: kbID, Status: types.KBCloneStatusPending,
		Total: len(items), BatchesRemaining: (len(items) + knowledgeImageCompressionBatchSize - 1) / knowledgeImageCompressionBatchSize,
		CreatedAt: now, UpdatedAt: now,
		Message: "Waiting for image compression worker",
	}
	for _, item := range items {
		progress.BytesBefore += item.FileSize
	}
	if len(items) == 0 {
		progress.Status = types.KBCloneStatusCompleted
		progress.Message = "No completed knowledge images require compression"
		_ = s.saveKnowledgeImageCompressionProgress(ctx, progress)
		s.releaseKnowledgeImageCompressionLock(ctx, kbID, taskID)
		return taskID, nil
	}
	if err := s.saveKnowledgeImageCompressionProgress(ctx, progress); err != nil {
		s.releaseKnowledgeImageCompressionLock(ctx, kbID, taskID)
		return "", err
	}
	for start := 0; start < len(items); start += knowledgeImageCompressionBatchSize {
		end := min(start+knowledgeImageCompressionBatchSize, len(items))
		ids := make([]string, 0, end-start)
		for _, item := range items[start:end] {
			ids = append(ids, item.ID)
		}
		payload := types.KnowledgeImageCompressionPayload{
			TenantID: types.MustTenantIDFromContext(ctx), TaskID: taskID, KBID: kbID, KnowledgeIDs: ids,
		}
		if err := s.enqueueKnowledgeImageCompression(payload, 0); err != nil {
			progress.Status = types.KBCloneStatusFailed
			progress.Message = "Failed to enqueue image compression task"
			progress.UpdatedAt = time.Now().Unix()
			_ = s.saveKnowledgeImageCompressionProgress(ctx, progress)
			return "", err
		}
	}
	return taskID, nil
}

func (s *knowledgeService) enqueueKnowledgeImageCompression(payload types.KnowledgeImageCompressionPayload, delay time.Duration) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	opts := []asynq.Option{asynq.Queue(types.QueueLow), asynq.MaxRetry(0)}
	if delay > 0 {
		opts = append(opts, asynq.ProcessIn(delay))
	}
	_, err = s.task.Enqueue(asynq.NewTask(types.TypeKnowledgeImageCompress, data), opts...)
	return err
}

func (s *knowledgeService) ProcessKnowledgeImageCompression(ctx context.Context, task *asynq.Task) error {
	var payload types.KnowledgeImageCompressionPayload
	if err := json.Unmarshal(task.Payload(), &payload); err != nil {
		return fmt.Errorf("unmarshal knowledge image compression payload: %w", err)
	}
	// The normal low-priority worker has one consumer, but Lite mode dispatches
	// tasks in goroutines. Serialize rounds so the shared progress and the
	// knowledge-base lock have the same behavior in both runtimes.
	s.imageCompressionMu.Lock()
	defer s.imageCompressionMu.Unlock()
	keepLock := true
	batchStarted := false
	defer func() {
		if batchStarted {
			if progress, err := s.GetKnowledgeImageCompressionProgress(context.WithoutCancel(ctx), payload.TaskID); err == nil && progress.ActiveBatches > 0 {
				progress.ActiveBatches--
				if progress.ActiveBatches == 0 && progress.BatchesRemaining == 0 {
					progress.Status = types.KBCloneStatusCompleted
					if progress.Succeeded == 0 && progress.Skipped == 0 && (progress.Failed+progress.PermanentFailed) > 0 {
						progress.Status = types.KBCloneStatusFailed
					}
					progress.Message = fmt.Sprintf("Compression finished: %d succeeded, %d skipped, %d failed", progress.Succeeded, progress.Skipped, progress.Failed+progress.PermanentFailed)
				}
				progress.UpdatedAt = time.Now().Unix()
				_ = s.saveKnowledgeImageCompressionProgress(context.WithoutCancel(ctx), progress)
			}
		}
		if !keepLock {
			s.releaseKnowledgeImageCompressionLock(context.WithoutCancel(ctx), payload.KBID, payload.TaskID)
		}
	}()
	ctx = context.WithValue(ctx, types.TenantIDContextKey, payload.TenantID)
	if s.tenantRepo != nil {
		tenant, tenantErr := s.tenantRepo.GetTenantByID(ctx, payload.TenantID)
		if tenantErr != nil {
			return fmt.Errorf("load tenant storage configuration: %w", tenantErr)
		}
		ctx = context.WithValue(ctx, types.TenantInfoContextKey, tenant)
	}
	progress, err := s.GetKnowledgeImageCompressionProgress(ctx, payload.TaskID)
	if err != nil {
		return err
	}
	progress.Status = types.KBCloneStatusProcessing
	progress.ActiveBatches++
	batchStarted = true
	if progress.Errors == nil {
		progress.Errors = make(map[string]string)
	}
	progress.RetryRound = payload.RetryRound
	progress.Retrying = 0
	progress.Message = fmt.Sprintf("Compressing round %d", payload.RetryRound+1)
	progress.UpdatedAt = time.Now().Unix()
	_ = s.saveKnowledgeImageCompressionProgress(ctx, progress)

	retryIDs := make([]string, 0)
	for _, knowledgeID := range payload.KnowledgeIDs {
		before, after, skipped, itemErr := s.compressExistingKnowledgeImage(ctx, payload.KBID, knowledgeID)
		switch {
		case itemErr == nil && skipped:
			progress.Skipped++
			delete(progress.Errors, knowledgeID)
		case itemErr == nil:
			progress.Succeeded++
			progress.BytesAfter += after
			progress.SavedBytes += before - after
			delete(progress.Errors, knowledgeID)
		case imagecompression.IsPermanent(itemErr):
			progress.PermanentFailed++
			progress.FailedKnowledgeIDs = appendUniqueString(progress.FailedKnowledgeIDs, knowledgeID)
			progress.Errors[knowledgeID] = itemErr.Error()
			logger.Warnf(ctx, "knowledge image %s cannot be compressed safely: %v", knowledgeID, itemErr)
		default:
			retryIDs = append(retryIDs, knowledgeID)
			progress.Errors[knowledgeID] = itemErr.Error()
			logger.Errorf(ctx, "knowledge image %s compression failed in round %d: %v", knowledgeID, payload.RetryRound, itemErr)
		}
		progress.Processed = progress.Succeeded + progress.Skipped + progress.PermanentFailed + progress.Failed
		progress.UpdatedAt = time.Now().Unix()
		_ = s.saveKnowledgeImageCompressionProgress(ctx, progress)
	}

	if len(retryIDs) > 0 && payload.RetryRound < knowledgeImageCompressionMaxRetryRounds {
		next := payload
		next.KnowledgeIDs = retryIDs
		next.RetryRound++
		progress.Retrying = len(retryIDs)
		progress.Message = fmt.Sprintf("Round complete; %d images scheduled for retry round %d", len(retryIDs), next.RetryRound)
		progress.UpdatedAt = time.Now().Unix()
		if err := s.enqueueKnowledgeImageCompression(next, knowledgeImageCompressionRetryDelay[payload.RetryRound]); err != nil {
			progress.Failed += len(retryIDs)
			for _, id := range retryIDs {
				progress.FailedKnowledgeIDs = appendUniqueString(progress.FailedKnowledgeIDs, id)
			}
			progress.Retrying = 0
			progress.Processed = progress.Succeeded + progress.Skipped + progress.PermanentFailed + progress.Failed
			progress.Status = types.KBCloneStatusFailed
			progress.Message = "Could not schedule the next retry round"
			progress.UpdatedAt = time.Now().Unix()
			completed, _ := s.finishKnowledgeImageCompressionBatch(ctx, progress)
			if completed {
				keepLock = false
			}
			return err
		}
		return s.saveKnowledgeImageCompressionProgress(ctx, progress)
	}

	if len(retryIDs) > 0 {
		progress.Failed += len(retryIDs)
		for _, id := range retryIDs {
			progress.FailedKnowledgeIDs = appendUniqueString(progress.FailedKnowledgeIDs, id)
		}
	}
	progress.Processed = progress.Succeeded + progress.Skipped + progress.PermanentFailed + progress.Failed
	progress.Retrying = 0
	completed, err := s.finishKnowledgeImageCompressionBatch(ctx, progress)
	if completed {
		keepLock = false
	}
	return err
}

// finishKnowledgeImageCompressionBatch advances task-level progress only after
// a batch has either succeeded or exhausted its grouped retry rounds.
func (s *knowledgeService) finishKnowledgeImageCompressionBatch(ctx context.Context, progress *types.KnowledgeImageCompressionProgress) (bool, error) {
	if progress.BatchesRemaining > 0 {
		progress.BatchesRemaining--
	}
	progress.UpdatedAt = time.Now().Unix()
	if progress.BatchesRemaining > 0 {
		progress.Status = types.KBCloneStatusProcessing
		progress.Message = fmt.Sprintf("Waiting for %d remaining image compression batches", progress.BatchesRemaining)
		return false, s.saveKnowledgeImageCompressionProgress(ctx, progress)
	}
	if progress.ActiveBatches > 1 {
		progress.Status = types.KBCloneStatusProcessing
		progress.Message = "Waiting for active image compression batches"
		return false, s.saveKnowledgeImageCompressionProgress(ctx, progress)
	}
	return true, s.saveKnowledgeImageCompressionProgress(ctx, progress)
}

func (s *knowledgeService) compressExistingKnowledgeImage(ctx context.Context, kbID, knowledgeID string) (int64, int64, bool, error) {
	tenantID := types.MustTenantIDFromContext(ctx)
	knowledge, err := s.repo.GetKnowledgeByID(ctx, tenantID, knowledgeID)
	if err != nil {
		return 0, 0, false, err
	}
	if knowledge.KnowledgeBaseID != kbID || knowledge.ParseStatus != types.ParseStatusCompleted || !IsImageType(knowledge.FileType) {
		return 0, 0, true, nil
	}
	kb, err := s.kbService.GetKnowledgeBaseByID(ctx, kbID)
	if err != nil {
		return 0, 0, false, err
	}
	fileSvc := s.resolveFileService(ctx, kb)
	if cleanupPath := compressionCleanupPath(knowledge.CompressionInfo); cleanupPath != "" {
		cleanupSvc := s.resolveFileServiceForPath(ctx, kb, cleanupPath)
		if err := cleanupSvc.DeleteFile(ctx, cleanupPath); err != nil {
			return 0, 0, false, fmt.Errorf("delete superseded original object: %w", err)
		}
		info := compressionInfoMap(knowledge.CompressionInfo)
		before := compressionInfoInt64(info, "original_size")
		after := compressionInfoInt64(info, "stored_size")
		delete(info, "original_path")
		info["status"] = "completed"
		data, _ := json.Marshal(info)
		if err := s.repo.UpdateKnowledgeColumns(ctx, knowledge.ID, map[string]interface{}{"compression_info": types.JSON(data)}); err != nil {
			return 0, 0, false, err
		}
		return before, after, false, nil
	}
	if knowledge.FileSize <= int64(imagecompression.DefaultConfig().TargetBytes) {
		return knowledge.FileSize, knowledge.FileSize, true, nil
	}

	sourceFileSvc := s.resolveFileServiceForPath(ctx, kb, knowledge.FilePath)
	reader, err := sourceFileSvc.GetFile(ctx, knowledge.FilePath)
	if err != nil {
		return 0, 0, false, fmt.Errorf("read source object: %w", err)
	}
	data, readErr := io.ReadAll(io.LimitReader(reader, knowledgeImageCompressionReadLimit+1))
	closeErr := reader.Close()
	if readErr != nil {
		return 0, 0, false, fmt.Errorf("read source object: %w", readErr)
	}
	if closeErr != nil {
		return 0, 0, false, fmt.Errorf("close source object: %w", closeErr)
	}
	if len(data) > knowledgeImageCompressionReadLimit {
		return 0, 0, false, &imagecompression.PermanentError{Reason: "source image exceeds 100MiB safety limit"}
	}
	result, err := imagecompression.Compress(data, knowledge.FileName, imagecompression.DefaultConfig())
	if err != nil {
		return 0, 0, false, err
	}
	if !result.Compressed {
		return result.OriginalSize, result.CompressedSize, true, nil
	}
	newPath, err := fileSvc.SaveBytes(ctx, result.Data, tenantID, result.FileName, false)
	if err != nil {
		return 0, 0, false, fmt.Errorf("save compressed object: %w", err)
	}
	info := compressionInfoMap(buildImageCompressionInfo(result, "historical_migration"))
	info["status"] = "cleanup_pending"
	info["original_path"] = knowledge.FilePath
	infoData, _ := json.Marshal(info)
	updates := map[string]interface{}{
		"file_path": newPath, "file_name": result.FileName, "file_type": result.Format,
		"file_size": result.CompressedSize, "compression_info": types.JSON(infoData), "updated_at": time.Now(),
	}
	if err := s.repo.UpdateKnowledgeColumns(ctx, knowledge.ID, updates); err != nil {
		_ = fileSvc.DeleteFile(ctx, newPath)
		return 0, 0, false, fmt.Errorf("switch knowledge object: %w", err)
	}
	if err := sourceFileSvc.DeleteFile(ctx, knowledge.FilePath); err != nil {
		return 0, 0, false, fmt.Errorf("delete original object after switch: %w", err)
	}
	info["status"] = "completed"
	delete(info, "original_path")
	infoData, _ = json.Marshal(info)
	if err := s.repo.UpdateKnowledgeColumns(ctx, knowledge.ID, map[string]interface{}{"compression_info": types.JSON(infoData)}); err != nil {
		logger.Warnf(ctx, "compressed image %s but failed to finalize compression metadata: %v", knowledge.ID, err)
	}
	// Update chunks table to reference the new compressed image path
	if err := s.updateChunksImagePath(ctx, knowledge.ID, knowledge.FilePath, newPath); err != nil {
		logger.Warnf(ctx, "compressed image %s but failed to update chunks: %v", knowledge.ID, err)
	}
	return result.OriginalSize, result.CompressedSize, false, nil
}

func compressionInfoMap(raw types.JSON) map[string]interface{} {
	result := make(map[string]interface{})
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &result)
	}
	return result
}

func compressionCleanupPath(raw types.JSON) string {
	info := compressionInfoMap(raw)
	if info["status"] != "cleanup_pending" {
		return ""
	}
	path, _ := info["original_path"].(string)
	return path
}

func compressionInfoInt64(info map[string]interface{}, key string) int64 {
	switch value := info[key].(type) {
	case float64:
		return int64(value)
	case int64:
		return value
	case int:
		return int64(value)
	default:
		return 0
	}
}

func appendUniqueString(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func (s *knowledgeService) saveKnowledgeImageCompressionProgress(ctx context.Context, progress *types.KnowledgeImageCompressionProgress) error {
	copyValue := *progress
	s.memImageCompressionProgress.Store(progress.TaskID, &copyValue)
	if s.redisClient == nil {
		return nil
	}
	data, err := json.Marshal(progress)
	if err != nil {
		return err
	}
	return s.redisClient.Set(ctx, knowledgeImageCompressionProgressPrefix+progress.TaskID, data, knowledgeImageCompressionProgressTTL).Err()
}

func (s *knowledgeService) GetKnowledgeImageCompressionProgress(ctx context.Context, taskID string) (*types.KnowledgeImageCompressionProgress, error) {
	if s.redisClient != nil {
		data, err := s.redisClient.Get(ctx, knowledgeImageCompressionProgressPrefix+taskID).Bytes()
		if err == nil {
			var progress types.KnowledgeImageCompressionProgress
			if err := json.Unmarshal(data, &progress); err != nil {
				return nil, err
			}
			return &progress, nil
		}
		if !errors.Is(err, redis.Nil) {
			return nil, err
		}
	}
	if value, ok := s.memImageCompressionProgress.Load(taskID); ok {
		progress := *(value.(*types.KnowledgeImageCompressionProgress))
		return &progress, nil
	}
	return nil, werrors.NewNotFoundError("Image compression task not found")
}

func (s *knowledgeService) acquireKnowledgeImageCompressionLock(ctx context.Context, kbID, taskID string) (bool, error) {
	if _, loaded := s.memImageCompressionRunning.LoadOrStore(kbID, taskID); loaded {
		return false, nil
	}
	if s.redisClient == nil {
		return true, nil
	}
	ok, err := s.redisClient.SetNX(ctx, knowledgeImageCompressionLockPrefix+kbID, taskID, knowledgeImageCompressionProgressTTL).Result()
	if err != nil || !ok {
		s.memImageCompressionRunning.Delete(kbID)
	}
	return ok, err
}

func (s *knowledgeService) releaseKnowledgeImageCompressionLock(ctx context.Context, kbID, taskID string) {
	if value, ok := s.memImageCompressionRunning.Load(kbID); ok && value == taskID {
		s.memImageCompressionRunning.Delete(kbID)
	}
	if s.redisClient == nil {
		return
	}
	// Compare-and-delete avoids releasing a newer task's lock after an old
	// worker wakes up late.
	const script = `if redis.call("get", KEYS[1]) == ARGV[1] then return redis.call("del", KEYS[1]) else return 0 end`
	if err := s.redisClient.Eval(ctx, script, []string{knowledgeImageCompressionLockPrefix + kbID}, taskID).Err(); err != nil {
		logger.Warnf(ctx, "failed to release image compression lock for KB %s: %v", kbID, err)
	}
}

// updateChunksImagePath updates all chunks of a knowledge item to replace the old image path with the new one
func (s *knowledgeService) updateChunksImagePath(ctx context.Context, knowledgeID, oldPath, newPath string) error {
	tenantID := types.MustTenantIDFromContext(ctx)
	chunks, err := s.chunkRepo.ListChunksByKnowledgeID(ctx, tenantID, knowledgeID)
	if err != nil {
		return fmt.Errorf("list chunks for knowledge %s: %w", knowledgeID, err)
	}

	// Find chunks that reference the old path and update them
	var chunksToUpdate []*types.Chunk
	for _, chunk := range chunks {
		if chunk.Content != "" && strings.Contains(chunk.Content, oldPath) {
			chunk.Content = strings.ReplaceAll(chunk.Content, oldPath, newPath)
			chunksToUpdate = append(chunksToUpdate, chunk)
		}
	}

	if len(chunksToUpdate) > 0 {
		if err := s.chunkRepo.UpdateChunks(ctx, chunksToUpdate); err != nil {
			return fmt.Errorf("update chunks for knowledge %s: %w", knowledgeID, err)
		}
		logger.Infof(ctx, "updated %d chunks for knowledge %s to reference new image path", len(chunksToUpdate), knowledgeID)
	}

	return nil
}
