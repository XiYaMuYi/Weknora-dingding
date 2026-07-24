package dingtalk

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/datasource"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
)

var _ datasource.Connector = (*Connector)(nil)

const dingtalkContentParserVersion = "3"

// Connector implements datasource.Connector for DingTalk online documents.
type Connector struct{}

func NewConnector() *Connector { return &Connector{} }

func (c *Connector) Type() string { return types.ConnectorTypeDingTalk }

func (c *Connector) Validate(ctx context.Context, config *types.DataSourceConfig) error {
	cfg, settings, err := parseDingTalkConfig(config)
	if err != nil {
		return err
	}
	client := NewClient(cfg)
	if err := client.Ping(ctx); err != nil {
		return fmt.Errorf("dingtalk connection failed: %w", err)
	}
	// If the user manually configured resource IDs, validation only checks the
	// token. Listing spaces/workspaces may require broader browse permissions
	// than syncing an already-known node.
	if len(config.ResourceIDs) > 0 {
		return nil
	}
	if settings.DingTalkType == "wiki" {
		if _, err := client.ListWikiWorkspaces(ctx); err != nil {
			return fmt.Errorf("dingtalk wiki access failed: %w", err)
		}
		return nil
	}
	if _, err := client.ListSpaces(ctx); err != nil {
		return fmt.Errorf("dingtalk drive access failed: %w", err)
	}
	return nil
}

func (c *Connector) ListResources(
	ctx context.Context, config *types.DataSourceConfig, parentID string,
) ([]types.Resource, error) {
	cfg, settings, err := parseDingTalkConfig(config)
	if err != nil {
		return nil, err
	}
	client := NewClient(cfg)

	if settings.DingTalkType == "wiki" {
		return c.listWikiResources(ctx, client, parentID)
	}

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
			if !isSyncableDriveDentry(d) {
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
	cfg, settings, err := parseDingTalkConfig(config)
	if err != nil {
		return nil, err
	}
	client := NewClient(cfg)

	if settings.DingTalkType == "wiki" {
		return c.resolveWikiResourceAncestors(ctx, client, resourceIDs)
	}

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

func (c *Connector) listWikiResources(ctx context.Context, client *Client, parentID string) ([]types.Resource, error) {
	if parentID == "" {
		workspaces, err := client.ListWikiWorkspaces(ctx)
		if err != nil {
			return nil, err
		}
		out := make([]types.Resource, 0, len(workspaces))
		for _, ws := range workspaces {
			out = append(out, types.Resource{
				ExternalID:  makeWikiWorkspaceResourceID(ws.WorkspaceID),
				Name:        ws.Name,
				Type:        "wiki_space",
				URL:         ws.URL,
				HasChildren: ws.RootNodeID != "",
				Metadata: map[string]interface{}{
					"workspace_id": ws.WorkspaceID,
					"root_node_id": ws.RootNodeID,
				},
			})
		}
		return out, nil
	}

	workspaceID, nodeID := parseWikiResourceID(parentID)
	if workspaceID == "" {
		return nil, fmt.Errorf("invalid wiki parent resource id: %s", parentID)
	}
	if nodeID == "" {
		rootNodeID, err := c.findWikiRootNodeID(ctx, client, workspaceID)
		if err != nil {
			return nil, err
		}
		nodeID = rootNodeID
	}

	nodes, _, err := client.ListWikiNodes(ctx, nodeID, "")
	if err != nil {
		return nil, err
	}
	out := make([]types.Resource, 0, len(nodes))
	for _, n := range nodes {
		resType := "doc"
		hasChildren := false
		if strings.EqualFold(n.Type, "FOLDER") {
			resType = "folder"
			hasChildren = true
		}
		out = append(out, types.Resource{
			ExternalID:  makeWikiNodeResourceID(workspaceID, n.NodeID),
			Name:        n.Name,
			Type:        resType,
			URL:         n.URL,
			ParentID:    parentID,
			HasChildren: hasChildren,
			ModifiedAt:  parseUpdatedTime(n.UpdatedTime),
			Metadata: map[string]interface{}{
				"workspace_id": workspaceID,
				"node_id":      n.NodeID,
				"category":     n.Category,
			},
		})
	}
	return out, nil
}

func (c *Connector) resolveWikiResourceAncestors(ctx context.Context, client *Client, resourceIDs []string) ([]string, error) {
	seen := make(map[string]bool)
	var ancestors []string
	for _, rid := range resourceIDs {
		workspaceID, nodeID := parseWikiResourceID(rid)
		if workspaceID == "" || nodeID == "" {
			continue
		}
		spaceRID := makeWikiWorkspaceResourceID(workspaceID)
		if !seen[spaceRID] {
			seen[spaceRID] = true
			ancestors = append(ancestors, spaceRID)
		}
		// DingTalk's node detail API may omit parent lineage, so the workspace
		// ancestor is the reliable minimum needed to reveal a saved selection.
		if _, err := client.GetWikiNode(ctx, nodeID); err != nil {
			logger.Warnf(ctx, "[DingTalk] resolve wiki ancestor get node %s: %v", nodeID, err)
		}
	}
	return ancestors, nil
}

func (c *Connector) findWikiRootNodeID(ctx context.Context, client *Client, workspaceID string) (string, error) {
	workspaces, err := client.ListWikiWorkspaces(ctx)
	if err != nil {
		return "", err
	}
	for _, ws := range workspaces {
		if ws.WorkspaceID == workspaceID {
			if ws.RootNodeID == "" {
				return "", fmt.Errorf("wiki workspace %s has empty root node id", workspaceID)
			}
			return ws.RootNodeID, nil
		}
	}
	return "", fmt.Errorf("wiki workspace %s not found", workspaceID)
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
		LastSyncTime:  time.Now().UTC(),
		ParserVersion: dingtalkContentParserVersion,
		DocRevisions:  make(map[string]string),
	}
	parserChanged := incremental && prev != nil && prev.ParserVersion != dingtalkContentParserVersion
	if !parserChanged && prev != nil && prev.DocRevisions != nil {
		for k, v := range prev.DocRevisions {
			newCursor.DocRevisions[k] = v
		}
	}

	var items []types.FetchedItem
	seenDocs := make(map[string]docRef)

	for _, rid := range resourceIDs {
		var refs []docRef
		var err error
		if settings.DingTalkType == "wiki" {
			refs, err = c.collectWikiDocRefs(ctx, client, rid, settings.IncludeSubfolders)
		} else {
			spaceID, rootDentry := parseResourceID(rid)
			if spaceID == "" {
				continue
			}
			refs, err = c.collectDriveDocRefs(ctx, client, spaceID, rootDentry, settings.IncludeSubfolders)
		}
		if err != nil {
			var partialErr *partialDocRefListError
			if !errors.As(err, &partialErr) {
				return nil, nil, fmt.Errorf("collect docs for %s: %w", rid, err)
			}
			items = appendDingTalkDocListFailureItems(items, partialErr.Failures)
		}
		for _, ref := range refs {
			seenDocs[ref.externalID] = ref
		}
	}

	for _, ref := range seenDocs {
		rev := ref.revision

		if incremental && !parserChanged && prev != nil && prev.DocRevisions != nil {
			if old, ok := prev.DocRevisions[ref.externalID]; ok && old == rev {
				continue
			}
		}

		var content []byte
		var fileName string
		var err error
		if ref.kind == dingtalkDocumentKindUploadedFile {
			items = append(items, skippedUploadedFileItem(ref))
			continue
		}
		if ref.kind == dingtalkDocumentKindSkipped || ref.kind == dingtalkDocumentKindUnsupported {
			items = append(items, skippedUnsupportedOnlineItem(ref))
			continue
		}
		if ref.docType == "wiki" {
			switch ref.kind {
			case dingtalkDocumentKindNativeSheet:
				content, fileName, err = client.DownloadWikiSpreadsheetContent(ctx, ref.dentryID, ref.name)
			case dingtalkDocumentKindNativeDoc, "":
				content, fileName, err = client.DownloadWikiDocContent(ctx, ref.dentryID, ref.name, ref.extension)
			default:
				err = fmt.Errorf("暂不支持同步该钉钉知识库节点类型：%s", ref.kind)
			}
		} else {
			content, fileName, err = client.DownloadDocContent(ctx, ref.spaceID, ref.dentryID, ref.name, ref.extension)
		}
		if err != nil {
			items = append(items, types.FetchedItem{
				ExternalID:       ref.externalID,
				Title:            ref.name,
				SourceResourceID: ref.sourceResourceID,
				Metadata: map[string]string{
					"error":             err.Error(),
					"dingtalk_doc_type": ref.metadataDocType(),
					"dingtalk_doc_kind": string(ref.kind),
				},
			})
			continue
		}
		newCursor.DocRevisions[ref.externalID] = rev
		items = append(items, types.FetchedItem{
			ExternalID:       ref.externalID,
			Title:            ref.name,
			FileName:         fileName,
			Content:          content,
			ContentType:      contentTypeForFileName(fileName),
			URL:              ref.itemURL(),
			UpdatedAt:        ref.updatedAt,
			SourceResourceID: ref.sourceResourceID,
			Metadata: map[string]string{
				"dingtalk_doc_type": ref.metadataDocType(),
				"dingtalk_doc_kind": string(ref.kind),
				"space_id":          ref.spaceID,
				"dentry_id":         ref.dentryID,
			},
		})
	}

	if incremental && prev != nil && prev.DocRevisions != nil {
		for externalID := range prev.DocRevisions {
			if _, ok := seenDocs[externalID]; ok {
				continue
			}
			delete(newCursor.DocRevisions, externalID)
			items = append(items, types.FetchedItem{
				ExternalID: externalID,
				IsDeleted:  true,
			})
		}
	}

	next := &types.SyncCursor{
		LastSyncTime: newCursor.LastSyncTime,
		ConnectorCursor: map[string]interface{}{
			"last_sync_time": newCursor.LastSyncTime,
			"parser_version": newCursor.ParserVersion,
			"doc_revisions":  newCursor.DocRevisions,
		},
	}
	return items, next, nil
}

type docRefListFailure struct {
	Ref docRef
	Err error
}

type partialDocRefListError struct {
	Failures []docRefListFailure
}

func (e *partialDocRefListError) Error() string {
	if e == nil || len(e.Failures) == 0 {
		return "partial dingtalk document listing failed"
	}
	parts := make([]string, 0, len(e.Failures))
	for _, failure := range e.Failures {
		parts = append(parts, failure.Err.Error())
	}
	return strings.Join(parts, "; ")
}

func appendDingTalkDocListFailureItems(items []types.FetchedItem, failures []docRefListFailure) []types.FetchedItem {
	for _, failure := range failures {
		ref := failure.Ref
		title := ref.name
		if title == "" {
			title = ref.dentryID
		}
		meta := map[string]string{
			"error":             failure.Err.Error(),
			"channel":           types.ChannelDingtalk,
			"dingtalk_doc_type": ref.metadataDocType(),
			"failure_stage":     "list_children",
		}
		if ref.spaceID != "" {
			meta["space_id"] = ref.spaceID
		}
		if ref.dentryID != "" {
			meta["dentry_id"] = ref.dentryID
		}
		items = append(items, types.FetchedItem{
			ExternalID:       ref.externalID,
			Title:            title,
			SourceResourceID: ref.sourceResourceID,
			Metadata:         meta,
		})
	}
	return items
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
	url              string
	docType          string
	kind             dingtalkDocumentKind
}

func (r docRef) itemURL() string {
	if r.url != "" {
		return r.url
	}
	return fmt.Sprintf("https://alidocs.dingtalk.com/i/nodes/%s", r.dentryID)
}

func (r docRef) metadataDocType() string {
	if r.docType != "" {
		return r.docType
	}
	return "doc"
}

type dingtalkDocumentKind string

const (
	dingtalkDocumentKindNativeDoc    dingtalkDocumentKind = "native_doc"
	dingtalkDocumentKindNativeSheet  dingtalkDocumentKind = "native_sheet"
	dingtalkDocumentKindUploadedFile dingtalkDocumentKind = "uploaded_file"
	dingtalkDocumentKindUnsupported  dingtalkDocumentKind = "unsupported"
	dingtalkDocumentKindSkipped      dingtalkDocumentKind = "skipped"
)

func normalizeWikiDocumentKind(n wikiNodeItem) dingtalkDocumentKind {
	if !strings.EqualFold(n.Type, "FILE") {
		return dingtalkDocumentKindUnsupported
	}
	category := strings.ToUpper(strings.TrimSpace(n.Category))
	nameExt := wikiNodeNameExtension(n)
	ext := extensionFromWikiNode(n)
	if hasWikiUploadedFileLocator(n) {
		return dingtalkDocumentKindUploadedFile
	}
	if isDingTalkNativeSpreadsheetExtension(nameExt) {
		return dingtalkDocumentKindNativeSheet
	}
	if nameExt != "" && isDingTalkNativeOnlineDocExtension(nameExt) {
		return dingtalkDocumentKindNativeDoc
	}
	switch category {
	case "ALIDOC", "DOC", "DOCUMENT":
		if isSupportedUploadedFileExtension(nameExt) {
			return dingtalkDocumentKindUploadedFile
		}
		if nameExt != "" {
			return dingtalkDocumentKindSkipped
		}
		return dingtalkDocumentKindNativeDoc
	case "SPREADSHEET", "SHEET", "ALISHEET", "WORKBOOK", "AIOBJECT", "AIBASE", "AIFORM":
		return dingtalkDocumentKindNativeSheet
	case "WHITEBOARD", "MINDNOTE", "MINDMAP", "FORM", "SLIDES", "PRESENTATION", "PDF":
		return dingtalkDocumentKindSkipped
	default:
		if isSupportedUploadedFileExtension(ext) {
			return dingtalkDocumentKindUploadedFile
		}
		return dingtalkDocumentKindSkipped
	}
}

func hasWikiUploadedFileLocator(n wikiNodeItem) bool {
	return strings.TrimSpace(n.SpaceID) != "" && strings.TrimSpace(firstNonEmpty(n.DentryID, n.FileID)) != ""
}

func (c *Connector) collectDocRefs(
	ctx context.Context, client *Client, spaceID, rootDentryID string, recursive bool,
) ([]docRef, error) {
	return c.collectDriveDocRefs(ctx, client, spaceID, rootDentryID, recursive)
}

func (c *Connector) enrichWikiNodeForDownload(ctx context.Context, client *Client, n wikiNodeItem) wikiNodeItem {
	detail, err := client.GetWikiNode(ctx, n.NodeID)
	if err != nil || detail == nil {
		if err != nil {
			logger.Warnf(ctx, "[DingTalk] get wiki node detail %s failed: %v", n.NodeID, err)
		}
		return n
	}
	if detail.NodeID == "" {
		detail.NodeID = n.NodeID
	}
	if detail.WorkspaceID == "" {
		detail.WorkspaceID = n.WorkspaceID
	}
	if detail.Name == "" {
		detail.Name = n.Name
	}
	if detail.Type == "" {
		detail.Type = n.Type
	}
	if detail.Category == "" {
		detail.Category = n.Category
	}
	if detail.URL == "" {
		detail.URL = n.URL
	}
	if detail.UpdatedTime == "" {
		detail.UpdatedTime = n.UpdatedTime
	}
	if detail.Size == 0 {
		detail.Size = n.Size
	}
	return *detail
}

func (c *Connector) collectDriveDocRefs(
	ctx context.Context, client *Client, spaceID, rootDentryID string, recursive bool,
) ([]docRef, error) {
	var refs []docRef
	var failures []docRefListFailure
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
							failures = append(failures, docRefListFailure{
								Ref: docRef{
									externalID:       makeStableDocExternalID(spaceID, d.ID),
									spaceID:          spaceID,
									dentryID:         d.ID,
									name:             d.Name,
									revision:         revisionKey(d),
									updatedAt:        parseUpdatedTime(d.UpdatedTime),
									sourceResourceID: makeDentryResourceID(spaceID, d.ID),
								},
								Err: fmt.Errorf("list children of %s: %w", d.ID, err),
							})
							continue
						}
					}
					continue
				}
				if !isSyncableDriveDentry(d) {
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
					kind:             driveDocumentKind(d),
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
	if len(failures) > 0 {
		return refs, &partialDocRefListError{Failures: failures}
	}
	return refs, nil
}

func (c *Connector) collectWikiDocRefs(
	ctx context.Context, client *Client, resourceID string, recursive bool,
) ([]docRef, error) {
	workspaceID, nodeID := parseWikiResourceID(resourceID)
	if workspaceID == "" {
		return nil, nil
	}
	if nodeID == "" {
		rootNodeID, err := c.findWikiRootNodeID(ctx, client, workspaceID)
		if err != nil {
			return nil, err
		}
		nodeID = rootNodeID
	}

	var refs []docRef
	var failures []docRefListFailure
	var walk func(parentNodeID string) error
	walk = func(parentNodeID string) error {
		cursor := ""
		for {
			nodes, next, err := client.ListWikiNodes(ctx, parentNodeID, cursor)
			if err != nil {
				return err
			}
			for _, n := range nodes {
				if strings.EqualFold(n.Type, "FOLDER") {
					if recursive {
						if err := walk(n.NodeID); err != nil {
							failures = append(failures, docRefListFailure{
								Ref: docRef{
									externalID:       makeStableWikiDocExternalID(workspaceID, n.NodeID),
									spaceID:          workspaceID,
									dentryID:         n.NodeID,
									name:             n.Name,
									extension:        extensionFromWikiNode(n),
									revision:         wikiRevisionKey(n),
									updatedAt:        parseUpdatedTime(n.UpdatedTime),
									sourceResourceID: makeWikiNodeResourceID(workspaceID, n.NodeID),
									url:              n.URL,
									docType:          "wiki",
								},
								Err: fmt.Errorf("list children of %s: %w", n.NodeID, err),
							})
							continue
						}
					}
					continue
				}
				if !strings.EqualFold(n.Type, "FILE") {
					continue
				}
				n = c.enrichWikiNodeForDownload(ctx, client, n)
				kind := normalizeWikiDocumentKind(n)
				if kind == dingtalkDocumentKindUnsupported {
					continue
				}
				spaceID := workspaceID
				dentryID := n.NodeID
				if kind == dingtalkDocumentKindUploadedFile {
					spaceID = firstNonEmpty(n.SpaceID, workspaceID)
					dentryID = firstNonEmpty(n.DentryID, n.FileID, n.NodeID)
				} else if n.DocKey != "" {
					dentryID = n.DocKey
				} else if n.WorkbookID != "" {
					dentryID = n.WorkbookID
				}
				refs = append(refs, docRef{
					externalID:       makeStableWikiDocExternalID(workspaceID, n.NodeID),
					spaceID:          spaceID,
					dentryID:         dentryID,
					name:             n.Name,
					extension:        extensionFromWikiNode(n),
					revision:         wikiRevisionKey(n),
					updatedAt:        parseUpdatedTime(n.UpdatedTime),
					sourceResourceID: makeWikiNodeResourceID(workspaceID, n.NodeID),
					url:              n.URL,
					docType:          "wiki",
					kind:             kind,
				})
			}
			if next == "" {
				break
			}
			cursor = next
		}
		return nil
	}
	if err := walk(nodeID); err != nil {
		return nil, err
	}
	if len(failures) > 0 {
		return refs, &partialDocRefListError{Failures: failures}
	}
	return refs, nil
}

func skippedUploadedFileItem(ref docRef) types.FetchedItem {
	return types.FetchedItem{
		ExternalID:       ref.externalID,
		Title:            ref.name,
		UpdatedAt:        ref.updatedAt,
		SourceResourceID: ref.sourceResourceID,
		Metadata: map[string]string{
			"channel":           types.ChannelDingtalk,
			"space_id":          ref.spaceID,
			"dentry_id":         ref.dentryID,
			"dingtalk_doc_type": ref.metadataDocType(),
			"dingtalk_doc_kind": string(ref.kind),
			"skip_reason":       dingtalkUploadedFileSkipReason(ref.name),
		},
	}
}

func skippedUnsupportedOnlineItem(ref docRef) types.FetchedItem {
	return types.FetchedItem{
		ExternalID:       ref.externalID,
		Title:            ref.name,
		UpdatedAt:        ref.updatedAt,
		SourceResourceID: ref.sourceResourceID,
		Metadata: map[string]string{
			"channel":           types.ChannelDingtalk,
			"space_id":          ref.spaceID,
			"dentry_id":         ref.dentryID,
			"dingtalk_doc_type": ref.metadataDocType(),
			"dingtalk_doc_kind": string(ref.kind),
			"skip_reason":       dingtalkUnsupportedOnlineSkipReason(ref.name, ref.extension),
		},
	}
}

func dingtalkUploadedFileSkipReason(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "该文件"
	}
	return fmt.Sprintf("%s 是上传到钉钉的附件文件，当前版本暂不支持同步上传附件。请在钉钉中将它转换为钉钉在线文档或在线表格后再重新同步。", name)
}

func dingtalkUnsupportedOnlineSkipReason(name, extension string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "该内容"
	}
	return fmt.Sprintf("%s 暂不支持同步%s。当前版本支持钉钉在线文档和在线表格；白板、脑图、表单、演示、PDF 阅读、CAD、模型、图片、音视频等内容请先整理或转换为钉钉在线文档/在线表格后再同步。", name, dingtalkUnsupportedTypeLabel(name, extension))
}

func dingtalkUnsupportedTypeLabel(name, extension string) string {
	s := strings.ToLower(strings.TrimSpace(name + " " + extension))
	switch {
	case strings.Contains(s, "whiteboard") || strings.Contains(s, "白板"):
		return "钉钉白板"
	case strings.Contains(s, "mind") || strings.Contains(s, "脑图"):
		return "钉钉脑图"
	case strings.Contains(s, "form") || strings.Contains(s, "表单"):
		return "钉钉表单"
	case strings.Contains(s, "slide") || strings.Contains(s, "presentation") || strings.Contains(s, "演示"):
		return "钉钉演示"
	case strings.Contains(s, "pdf"):
		return "钉钉 PDF 阅读内容"
	case strings.Contains(s, "cad") || strings.Contains(s, "dwg"):
		return "CAD/工程图"
	default:
		return "该在线类型"
	}
}

const (
	prefixSpace  = "space:"
	prefixDentry = ":dentry:"
	prefixWiki   = "wiki:"
	prefixNode   = ":node:"
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

func makeWikiWorkspaceResourceID(workspaceID string) string {
	return prefixWiki + workspaceID
}

func makeWikiNodeResourceID(workspaceID, nodeID string) string {
	return prefixWiki + workspaceID + prefixNode + nodeID
}

func makeStableWikiDocExternalID(workspaceID, nodeID string) string {
	return "wiki:" + workspaceID + ":" + nodeID
}

func parseWikiResourceID(rid string) (workspaceID, nodeID string) {
	rid = strings.TrimSpace(rid)
	if !strings.HasPrefix(rid, prefixWiki) {
		return "", ""
	}
	rest := strings.TrimPrefix(rid, prefixWiki)
	if idx := strings.Index(rest, prefixNode); idx >= 0 {
		return rest[:idx], rest[idx+len(prefixNode):]
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

func isSyncableDriveDentry(d dentryItem) bool {
	if !strings.EqualFold(d.Type, "FILE") {
		return false
	}
	return isOnlineDocDentry(d) || isSupportedUploadedFileExtension(d.Extension)
}

func driveDocumentKind(d dentryItem) dingtalkDocumentKind {
	if isDriveNativeOnlineDocExtension(d.Extension) {
		return dingtalkDocumentKindNativeDoc
	}
	return dingtalkDocumentKindUploadedFile
}

func isSupportedUploadedFileExtension(extension string) bool {
	ext := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(extension), "."))
	switch ext {
	case "pdf", "txt", "md", "markdown", "doc", "docx", "ppt", "pptx", "csv", "xlsx", "xls", "json",
		"epub", "mhtml", "jpg", "jpeg", "png", "gif", "mp3", "wav", "m4a", "flac", "ogg":
		return true
	default:
		return false
	}
}

func isOfficeFileExtension(extension string) bool {
	ext := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(extension), "."))
	switch ext {
	case "doc", "docx", "xls", "xlsx", "ppt", "pptx", "pdf", "csv":
		return true
	default:
		return false
	}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func revisionKey(d dentryItem) string {
	if d.UpdatedTime != "" {
		return d.UpdatedTime
	}
	return fmt.Sprintf("%s:%d", d.ID, d.Size)
}

func wikiRevisionKey(n wikiNodeItem) string {
	if n.UpdatedTime != "" {
		return n.UpdatedTime
	}
	return fmt.Sprintf("%s:%d", n.NodeID, n.Size)
}

func extensionFromWikiNode(n wikiNodeItem) string {
	if ext := wikiNodeNameExtension(n); ext != "" {
		return ext
	}
	switch strings.ToUpper(n.Category) {
	case "ALIDOC", "DOC":
		return "md"
	case "SPREADSHEET", "SHEET", "ALISHEET", "WORKBOOK":
		return "xlsx"
	default:
		return ""
	}
}

func wikiNodeNameExtension(n wikiNodeItem) string {
	name := strings.ToLower(strings.TrimSpace(n.Name))
	if idx := strings.LastIndex(name, "."); idx >= 0 && idx < len(name)-1 {
		return name[idx+1:]
	}
	return ""
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

func markdownFileName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "document.md"
	}
	if idx := strings.LastIndex(name, "."); idx > 0 {
		name = name[:idx]
	}
	return pickFileName(name, "md", "md")
}

func isDingTalkNativeOnlineDocExtension(extension string) bool {
	ext := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(extension), "."))
	switch ext {
	case "", "adoc":
		return true
	default:
		return false
	}
}

func isDriveNativeOnlineDocExtension(extension string) bool {
	ext := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(extension), "."))
	switch ext {
	case "", "doc", "docx", "adoc", "markdown", "md":
		return true
	default:
		return false
	}
}

func isDingTalkSpreadsheetExtension(extension string) bool {
	return isDingTalkNativeSpreadsheetExtension(extension) || isOfficeSpreadsheetExtension(extension)
}

func isDingTalkNativeSpreadsheetExtension(extension string) bool {
	ext := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(extension), "."))
	return ext == "axls"
}

func isOfficeSpreadsheetExtension(extension string) bool {
	ext := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(extension), "."))
	return ext == "xlsx" || ext == "xls"
}

func isDingTalkNotWorkbookError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "not workbook") ||
		strings.Contains(msg, "不是可读取的表格")
}

func contentTypeForFileName(fileName string) string {
	lower := strings.ToLower(fileName)
	switch {
	case strings.HasSuffix(lower, ".md"), strings.HasSuffix(lower, ".markdown"):
		return "text/markdown"
	case strings.HasSuffix(lower, ".docx"):
		return "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	case strings.HasSuffix(lower, ".xlsx"):
		return "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	case strings.HasSuffix(lower, ".xls"):
		return "application/vnd.ms-excel"
	case strings.HasSuffix(lower, ".pdf"):
		return "application/pdf"
	case strings.HasSuffix(lower, ".pptx"):
		return "application/vnd.openxmlformats-officedocument.presentationml.presentation"
	default:
		return "application/octet-stream"
	}
}
