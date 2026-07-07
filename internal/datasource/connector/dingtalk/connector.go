package dingtalk

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/datasource"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
)

var _ datasource.Connector = (*Connector)(nil)

// Connector implements datasource.Connector for DingTalk online documents.
type Connector struct{}

func NewConnector() *Connector { return &Connector{} }

func (c *Connector) Type() string { return types.ConnectorTypeDingTalk }

func (c *Connector) Validate(ctx context.Context, config *types.DataSourceConfig) error {
	cfg, _, err := parseDingTalkConfig(config)
	if err != nil {
		return err
	}
	if err := NewClient(cfg).Ping(ctx); err != nil {
		return fmt.Errorf("dingtalk connection failed: %w", err)
	}
	return nil
}

func (c *Connector) ListResources(
	ctx context.Context, config *types.DataSourceConfig, parentID string,
) ([]types.Resource, error) {
	cfg, _, err := parseDingTalkConfig(config)
	if err != nil {
		return nil, err
	}
	client := NewClient(cfg)

	if parentID == "" {
		spaces, err := client.ListSpaces(ctx)
		if err != nil {
			return nil, err
		}
		out := make([]types.Resource, 0, len(spaces))
		for _, sp := range spaces {
			out = append(out, types.Resource{
				ExternalID:  makeSpaceResourceID(sp.SpaceID),
				Name:        sp.SpaceName,
				Type:        "space",
				HasChildren: true,
				Metadata: map[string]interface{}{
					"space_type": sp.SpaceType,
				},
			})
		}
		return out, nil
	}

	spaceID, dentryID := parseResourceID(parentID)
	if spaceID == "" {
		return nil, fmt.Errorf("invalid parent resource id: %s", parentID)
	}

	parentDentry := dentryID
	dentries, _, err := client.ListDentries(ctx, spaceID, parentDentry, "")
	if err != nil {
		return nil, fmt.Errorf("list dentries: %w", err)
	}

	out := make([]types.Resource, 0, len(dentries))
	for _, d := range dentries {
		resType := "folder"
		if strings.EqualFold(d.Type, "FILE") {
			if !isOnlineDocDentry(d) {
				continue
			}
			resType = "doc"
		}
		mod := parseUpdatedTime(d.UpdatedTime)
		out = append(out, types.Resource{
			ExternalID:  makeDentryResourceID(spaceID, d.ID),
			Name:        d.Name,
			Type:        resType,
			ParentID:    parentID,
			HasChildren: strings.EqualFold(d.Type, "FOLDER"),
			ModifiedAt:  mod,
			Metadata: map[string]interface{}{
				"space_id":  spaceID,
				"dentry_id": d.ID,
				"extension": d.Extension,
			},
		})
	}
	return out, nil
}

func (c *Connector) ResolveResourceAncestors(
	ctx context.Context, config *types.DataSourceConfig, resourceIDs []string,
) ([]string, error) {
	cfg, _, err := parseDingTalkConfig(config)
	if err != nil {
		return nil, err
	}
	client := NewClient(cfg)

	seen := make(map[string]bool)
	var ancestors []string
	add := func(id string) {
		if id != "" && !seen[id] {
			seen[id] = true
			ancestors = append(ancestors, id)
		}
	}

	for _, rid := range resourceIDs {
		spaceID, dentryID := parseResourceID(rid)
		if spaceID == "" || dentryID == "" {
			continue
		}
		add(makeSpaceResourceID(spaceID))

		current := dentryID
		for i := 0; i < 32 && current != ""; i++ {
			d, err := client.GetDentry(ctx, spaceID, current)
			if err != nil {
				logger.Warnf(ctx, "[DingTalk] resolve ancestors get dentry %s:%s: %v", spaceID, current, err)
				break
			}
			if d.ParentID == "" {
				break
			}
			add(makeDentryResourceID(spaceID, d.ParentID))
			current = d.ParentID
		}
	}
	return ancestors, nil
}

func (c *Connector) FetchAll(ctx context.Context, config *types.DataSourceConfig, resourceIDs []string) ([]types.FetchedItem, error) {
	items, _, err := c.sync(ctx, config, resourceIDs, nil, false)
	return items, err
}

func (c *Connector) FetchIncremental(
	ctx context.Context, config *types.DataSourceConfig, cursor *types.SyncCursor,
) ([]types.FetchedItem, *types.SyncCursor, error) {
	resourceIDs := config.ResourceIDs
	if len(resourceIDs) == 0 {
		return nil, nil, fmt.Errorf("no resource IDs configured")
	}
	var prev dingtalkCursor
	if cursor != nil && cursor.ConnectorCursor != nil {
		b, _ := json.Marshal(cursor.ConnectorCursor)
		_ = json.Unmarshal(b, &prev)
	}
	return c.sync(ctx, config, resourceIDs, &prev, true)
}

func (c *Connector) sync(
	ctx context.Context,
	config *types.DataSourceConfig,
	resourceIDs []string,
	prev *dingtalkCursor,
	incremental bool,
) ([]types.FetchedItem, *types.SyncCursor, error) {
	cfg, settings, err := parseDingTalkConfig(config)
	if err != nil {
		return nil, nil, err
	}
	client := NewClient(cfg)

	newCursor := dingtalkCursor{
		LastSyncTime: time.Now().UTC(),
		DocRevisions: make(map[string]string),
	}
	if prev != nil && prev.DocRevisions != nil {
		for k, v := range prev.DocRevisions {
			newCursor.DocRevisions[k] = v
		}
	}

	var items []types.FetchedItem
	seenDocs := make(map[string]docRef)

	for _, rid := range resourceIDs {
		spaceID, rootDentry := parseResourceID(rid)
		if spaceID == "" {
			continue
		}
		refs, err := c.collectDocRefs(ctx, client, spaceID, rootDentry, settings.IncludeSubfolders)
		if err != nil {
			return nil, nil, fmt.Errorf("collect docs for %s: %w", rid, err)
		}
		for _, ref := range refs {
			seenDocs[ref.externalID] = ref
		}
	}

	for _, ref := range seenDocs {
		rev := ref.revision
		newCursor.DocRevisions[ref.externalID] = rev

		if incremental && prev != nil && prev.DocRevisions != nil {
			if old, ok := prev.DocRevisions[ref.externalID]; ok && old == rev {
				continue
			}
		}

		content, fileName, err := client.DownloadDocContent(ctx, ref.spaceID, ref.dentryID, ref.name, ref.extension)
		if err != nil {
			items = append(items, types.FetchedItem{
				ExternalID:       ref.externalID,
				Title:            ref.name,
				SourceResourceID: ref.sourceResourceID,
				Metadata: map[string]string{
					"error":              err.Error(),
					"dingtalk_doc_type":  "doc",
				},
			})
			continue
		}
		items = append(items, types.FetchedItem{
			ExternalID:       ref.externalID,
			Title:            ref.name,
			FileName:         fileName,
			Content:          content,
			ContentType:      contentTypeForFileName(fileName),
			URL:              fmt.Sprintf("https://alidocs.dingtalk.com/i/nodes/%s", ref.dentryID),
			UpdatedAt:        ref.updatedAt,
			SourceResourceID: ref.sourceResourceID,
			Metadata: map[string]string{
				"dingtalk_doc_type": "doc",
				"space_id":          ref.spaceID,
				"dentry_id":         ref.dentryID,
			},
		})
	}

	next := &types.SyncCursor{
		LastSyncTime: newCursor.LastSyncTime,
		ConnectorCursor: map[string]interface{}{
			"last_sync_time": newCursor.LastSyncTime,
			"doc_revisions":  newCursor.DocRevisions,
		},
	}
	return items, next, nil
}

type docRef struct {
	externalID       string
	spaceID          string
	dentryID         string
	name             string
	extension        string
	revision         string
	updatedAt        time.Time
	sourceResourceID string
}

func (c *Connector) collectDocRefs(
	ctx context.Context, client *Client, spaceID, rootDentryID string, recursive bool,
) ([]docRef, error) {
	var refs []docRef
	var walk func(parentID string) error
	walk = func(parentID string) error {
		cursor := ""
		for {
			dentries, next, err := client.ListDentries(ctx, spaceID, parentID, cursor)
			if err != nil {
				return err
			}
			for _, d := range dentries {
				if strings.EqualFold(d.Type, "FOLDER") {
					if recursive {
						if err := walk(d.ID); err != nil {
							return err
						}
					}
					continue
				}
				if !isOnlineDocDentry(d) {
					continue
				}
				extID := makeStableDocExternalID(spaceID, d.ID)
				refs = append(refs, docRef{
					externalID:       extID,
					spaceID:          spaceID,
					dentryID:         d.ID,
					name:             d.Name,
					extension:        d.Extension,
					revision:         revisionKey(d),
					updatedAt:        parseUpdatedTime(d.UpdatedTime),
					sourceResourceID: makeDentryResourceID(spaceID, d.ID),
				})
			}
			if next == "" {
				break
			}
			cursor = next
		}
		return nil
	}
	if err := walk(rootDentryID); err != nil {
		return nil, err
	}
	return refs, nil
}

const (
	prefixSpace  = "space:"
	prefixDentry = ":dentry:"
)

func makeSpaceResourceID(spaceID string) string {
	return prefixSpace + spaceID
}

func makeDentryResourceID(spaceID, dentryID string) string {
	return prefixSpace + spaceID + prefixDentry + dentryID
}

func makeStableDocExternalID(spaceID, dentryID string) string {
	return spaceID + ":" + dentryID
}

func parseResourceID(rid string) (spaceID, dentryID string) {
	rid = strings.TrimSpace(rid)
	if !strings.HasPrefix(rid, prefixSpace) {
		return "", ""
	}
	rest := strings.TrimPrefix(rid, prefixSpace)
	if idx := strings.Index(rest, prefixDentry); idx >= 0 {
		return rest[:idx], rest[idx+len(prefixDentry):]
	}
	return rest, ""
}

func isOnlineDocDentry(d dentryItem) bool {
	if !strings.EqualFold(d.Type, "FILE") {
		return false
	}
	ext := strings.ToLower(strings.TrimPrefix(d.Extension, "."))
	switch ext {
	case "", "doc", "docx", "adoc", "markdown", "md":
		return true
	default:
		return false
	}
}

func revisionKey(d dentryItem) string {
	if d.UpdatedTime != "" {
		return d.UpdatedTime
	}
	return fmt.Sprintf("%s:%d", d.ID, d.Size)
}

func parseUpdatedTime(raw string) time.Time {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t
	}
	if t, err := time.Parse(time.RFC3339Nano, raw); err == nil {
		return t
	}
	return time.Time{}
}

func pickFileName(name, extension, defaultExt string) string {
	name = strings.TrimSpace(name)
	ext := strings.TrimPrefix(strings.ToLower(extension), ".")
	if ext == "" {
		ext = defaultExt
	}
	if !strings.HasSuffix(strings.ToLower(name), "."+ext) {
		if name == "" {
			name = "document"
		}
		name = name + "." + ext
	}
	return name
}

func contentTypeForFileName(fileName string) string {
	lower := strings.ToLower(fileName)
	switch {
	case strings.HasSuffix(lower, ".md"), strings.HasSuffix(lower, ".markdown"):
		return "text/markdown"
	case strings.HasSuffix(lower, ".docx"):
		return "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	default:
		return "application/octet-stream"
	}
}