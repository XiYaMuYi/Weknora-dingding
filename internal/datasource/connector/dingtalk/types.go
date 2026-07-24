// Package dingtalk implements the DingTalk (钉钉) data source connector for WeKnora.
//
// It syncs online documents (Doc) from DingTalk document storage into knowledge bases.
//
// DingTalk Open API references (verify against your tenant's console permissions):
//   - Access token: https://open.dingtalk.com/document/orgapp/obtain-orgapp-token
//   - Storage / document: https://open.dingtalk.com/document/orgapp/document-overview
package dingtalk

import (
	"strings"
	"time"
)

// Config holds DingTalk connector credentials (enterprise internal app).
type Config struct {
	// AppKey is the application's AppKey (Client ID).
	AppKey string `json:"app_key"`
	// AppSecret is the application's AppSecret (Client Secret).
	AppSecret string `json:"app_secret"`
	// OperatorUnionID is the DingTalk member unionId used as the operation subject.
	OperatorUnionID string `json:"union_id,omitempty"`
	// CorpID is a legacy UI field. Older builds labelled this input as CorpId,
	// but DingTalk Drive APIs actually require an operator unionId.
	CorpID string `json:"corp_id,omitempty"`
	// BaseURL overrides the Open API host (for tests or regional endpoints).
	BaseURL string `json:"base_url,omitempty"`
	// LegacyBaseURL overrides oapi.dingtalk.com (gettoken fallback).
	LegacyBaseURL string `json:"legacy_base_url,omitempty"`
}

const (
	defaultAPIBase    = "https://api.dingtalk.com"
	defaultLegacyBase = "https://oapi.dingtalk.com"
)

func (c *Config) GetAPIBase() string {
	if c.BaseURL != "" {
		return c.BaseURL
	}
	return defaultAPIBase
}

func (c *Config) GetLegacyBase() string {
	if c.LegacyBaseURL != "" {
		return c.LegacyBaseURL
	}
	return defaultLegacyBase
}

// Settings from DataSourceConfig.Settings (non-secret).
type Settings struct {
	// IncludeSubfolders when true, Fetch* walks folder subtrees under selected resources.
	IncludeSubfolders bool `json:"include_subfolders"`
	// ExportFormat preferred export: "markdown" (when API supports) or "docx".
	ExportFormat string `json:"export_format"`
	// DingTalkType selects the backing document system: drive or wiki.
	DingTalkType string `json:"dingtalk_type"`
	// OperatorUnionID is non-secret and duplicated in settings so edit forms can
	// show it without exposing AppSecret.
	OperatorUnionID string `json:"union_id"`
}

func parseSettings(m map[string]interface{}) Settings {
	var s Settings
	if m == nil {
		s.IncludeSubfolders = true
		return s
	}
	if v, ok := m["include_subfolders"].(bool); ok {
		s.IncludeSubfolders = v
	} else {
		s.IncludeSubfolders = true
	}
	if v, ok := m["export_format"].(string); ok {
		s.ExportFormat = v
	}
	if v, ok := m["dingtalk_type"].(string); ok {
		s.DingTalkType = strings.ToLower(strings.TrimSpace(v))
	}
	if s.DingTalkType == "" {
		s.DingTalkType = "drive"
	}
	if v, ok := m["union_id"].(string); ok {
		s.OperatorUnionID = strings.TrimSpace(v)
	}
	return s
}

// --- API DTOs ---

type legacyTokenResponse struct {
	ErrCode     int    `json:"errcode"`
	ErrMsg      string `json:"errmsg"`
	AccessToken string `json:"access_token"`
	ExpiresIn   int    `json:"expires_in"`
}

type oauthTokenResponse struct {
	AccessToken string `json:"accessToken"`
	ExpireIn    int    `json:"expireIn"`
}

type apiErrorBody struct {
	Code           string `json:"code"`
	Message        string `json:"message"`
	RequestID      string `json:"requestid"`
	RequestIDCamel string `json:"requestId"`
}

type dentryIDByUUIDResponse struct {
	DentryUUID string `json:"dentryUuid"`
	DentryID   string `json:"dentryId"`
	SpaceID    string `json:"spaceId"`
}

type downloadInfoResponse struct {
	DownloadURL         string            `json:"downloadUrl"`
	ResourceURLs        []string          `json:"resourceUrls"`
	Headers             map[string]string `json:"headers"`
	HeaderSignatureInfo struct {
		ResourceURLs         []string          `json:"resourceUrls"`
		InternalResourceURLs []string          `json:"internalResourceUrls"`
		Headers              map[string]string `json:"headers"`
		ExpirationSeconds    int               `json:"expirationSeconds"`
	} `json:"headerSignatureInfo"`
}

// spaceItem is a DingTalk document space (team/personal drive root).
type spaceItem struct {
	SpaceID   string `json:"spaceId"`
	SpaceName string `json:"spaceName"`
	SpaceType string `json:"spaceType"`
}

type listSpacesResponse struct {
	Spaces    []spaceItem `json:"spaces"`
	Items     []spaceItem `json:"items"`
	NextToken string      `json:"nextToken"`
}

// dentryItem is a file or folder in a space.
type dentryItem struct {
	ID          string `json:"id"`
	DentryID    string `json:"dentryId"`
	DentryUUID  string `json:"dentryUuid"`
	Name        string `json:"name"`
	Type        string `json:"type"` // FILE | FOLDER
	Extension   string `json:"extension"`
	Size        int64  `json:"size"`
	UpdatedTime string `json:"updatedTime"` // ISO8601 or millis string per API version
	ParentID    string `json:"parentId"`
	SpaceID     string `json:"spaceId"`
}

type listDentriesResponse struct {
	Dentries   []dentryItem `json:"dentries"`
	NextCursor string       `json:"nextCursor"`
	Items      []dentryItem `json:"items"`
	NextToken  string       `json:"nextToken"`
}

type wikiWorkspaceItem struct {
	WorkspaceID string `json:"workspaceId"`
	Name        string `json:"name"`
	RootNodeID  string `json:"rootNodeId"`
	URL         string `json:"url"`
}

type listWikiWorkspacesResponse struct {
	Workspaces []wikiWorkspaceItem `json:"workspaces"`
	Items      []wikiWorkspaceItem `json:"items"`
	NextToken  string              `json:"nextToken"`
}

type wikiNodeItem struct {
	NodeID      string `json:"nodeId"`
	WorkspaceID string `json:"workspaceId"`
	Name        string `json:"name"`
	Type        string `json:"type"` // FILE | FOLDER
	Category    string `json:"category"`
	URL         string `json:"url"`
	Size        int64  `json:"size"`
	UpdatedTime string `json:"updatedTime"`
	SpaceID     string `json:"spaceId"`
	DentryID    string `json:"dentryId"`
	FileID      string `json:"fileId"`
	FileType    string `json:"fileType"`
	DocKey      string `json:"docKey"`
	WorkbookID  string `json:"workbookId"`
}

type listWikiNodesResponse struct {
	Nodes     []wikiNodeItem `json:"nodes"`
	Items     []wikiNodeItem `json:"items"`
	NextToken string         `json:"nextToken"`
}

type workbookSheetItem struct {
	SheetID     string `json:"sheetId"`
	ID          string `json:"id"`
	Name        string `json:"name"`
	Title       string `json:"title"`
	RowCount    int    `json:"rowCount"`
	ColumnCount int    `json:"columnCount"`
}

type listWorkbookSheetsResponse struct {
	Sheets    []workbookSheetItem `json:"sheets"`
	Items     []workbookSheetItem `json:"items"`
	Value     []workbookSheetItem `json:"value"`
	NextToken string              `json:"nextToken"`
}

// dingtalkCursor stores incremental sync state.
type dingtalkCursor struct {
	LastSyncTime  time.Time         `json:"last_sync_time"`
	ParserVersion string            `json:"parser_version"`
	DocRevisions  map[string]string `json:"doc_revisions"` // external_id -> revision key
}
