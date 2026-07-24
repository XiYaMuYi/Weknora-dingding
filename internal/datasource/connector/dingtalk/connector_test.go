package dingtalk

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
)

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func testDOCXBytes(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range map[string]string{
		"[Content_Types].xml": `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"/>`,
		"word/document.xml":   `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"/>`,
	} {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(w, content); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func zipBytesContainingOnly(t *testing.T, name, content string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(w, content); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestValidateDOCX_rejectsInvalidDownloads(t *testing.T) {
	tests := []struct {
		name string
		data []byte
	}{
		{name: "empty", data: nil},
		{name: "json error", data: []byte(`{"code":"Forbidden","message":"denied"}`)},
		{name: "html login page", data: []byte("<html><body>login</body></html>")},
		{name: "plain text", data: []byte("document text")},
		{name: "zip without word document", data: zipBytesContainingOnly(t, "other.txt", "x")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := validateDOCX(tt.data); err == nil {
				t.Fatal("validateDOCX() error = nil, want rejection")
			}
		})
	}
}

func TestValidateDOCX_acceptsDOCX(t *testing.T) {
	if err := validateDOCX(testDOCXBytes(t)); err != nil {
		t.Fatalf("validateDOCX() error = %v", err)
	}
}

func TestDownloadWikiDocContent_downloadsBackingDentryAsDOCX(t *testing.T) {
	wantDOCX := testDOCXBytes(t)
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	defer srv.Close()

	mux.HandleFunc("/v1.0/oauth2/accessToken", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, oauthTokenResponse{AccessToken: "test-token", ExpireIn: 7200})
	})
	mux.HandleFunc("/v2.0/doc/dentries/doc-key/queryDentryId", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, dentryIDByUUIDResponse{SpaceID: "space-1", DentryID: "dentry-1"})
	})
	mux.HandleFunc("/v1.0/storage/spaces/space-1/dentries/dentry-1/downloadInfos/query", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("download info method = %s, want POST", r.Method)
		}
		writeJSON(w, downloadInfoResponse{DownloadURL: srv.URL + "/download/document.docx"})
	})
	mux.HandleFunc("/download/document.docx", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.wordprocessingml.document")
		_, _ = w.Write(wantDOCX)
	})
	mux.HandleFunc("/v1.0/doc/documents/dentry-1/content", func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("legacy content endpoint must not be called")
	})
	mux.HandleFunc("/v1.0/doc/suites/documents/doc-key/blocks", func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("blocks endpoint must not be called")
	})

	client := NewClient(&Config{
		AppKey:          "key",
		AppSecret:       "secret",
		OperatorUnionID: "operator",
		BaseURL:         srv.URL,
	})
	data, fileName, err := client.DownloadWikiDocContent(context.Background(), "doc-key", "测试文档.adoc", "adoc")
	if err != nil {
		t.Fatal(err)
	}
	if fileName != "测试文档.docx" {
		t.Fatalf("fileName = %q, want %q", fileName, "测试文档.docx")
	}
	if !bytes.Equal(data, wantDOCX) {
		t.Fatal("returned bytes differ from downloaded DOCX")
	}
}

func fakeDingTalkServer(dentries []dentryItem) (*httptest.Server, *Config) {
	mux := http.NewServeMux()

	mux.HandleFunc("/v1.0/oauth2/accessToken", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, oauthTokenResponse{AccessToken: "test-token", ExpireIn: 7200})
	})

	mux.HandleFunc("/v1.0/drive/spaces", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, listSpacesResponse{
			Spaces: []spaceItem{{SpaceID: "sp1", SpaceName: "Team Drive", SpaceType: "org"}},
		})
	})

	mux.HandleFunc("/v1.0/storage/spaces/sp1/dentries", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, listDentriesResponse{Dentries: dentries})
	})
	mux.HandleFunc("/v1.0/storage/spaces/sp1/dentries/", func(w http.ResponseWriter, r *http.Request) {
		// GET single dentry (e.g. .../dentries/doc1)
		id := strings.TrimPrefix(r.URL.Path, "/v1.0/storage/spaces/sp1/dentries/")
		if strings.Contains(id, "/") {
			http.NotFound(w, r)
			return
		}
		for _, d := range dentries {
			if d.ID == id {
				writeJSON(w, d)
				return
			}
		}
		writeJSON(w, dentryItem{ID: id, ParentID: ""})
	})

	mux.HandleFunc("/v1.0/storage/spaces/sp1/dentries/doc1/downloadInfos", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]string{"downloadUrl": ""})
	})

	mux.HandleFunc("/v1.0/doc/documents/doc1/content", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]string{"content": "# Hello\n\nDingTalk doc body."})
	})

	srv := httptest.NewServer(mux)
	cfg := &Config{
		AppKey:    "key",
		AppSecret: "secret",
		BaseURL:   srv.URL,
	}
	return srv, cfg
}

func fakeDingTalkWikiServer(nodes []wikiNodeItem) (*httptest.Server, *Config) {
	mux := http.NewServeMux()

	mux.HandleFunc("/v1.0/oauth2/accessToken", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, oauthTokenResponse{AccessToken: "test-token", ExpireIn: 7200})
	})
	mux.HandleFunc("/v2.0/wiki/workspaces", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, listWikiWorkspacesResponse{
			Workspaces: []wikiWorkspaceItem{{
				WorkspaceID: "wk1",
				Name:        "Knowledge",
				RootNodeID:  "root1",
				URL:         "https://alidocs.dingtalk.com/wiki/wk1",
			}},
		})
	})
	mux.HandleFunc("/v2.0/wiki/nodes", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("parentNodeId") != "root1" {
			writeJSON(w, listWikiNodesResponse{})
			return
		}
		writeJSON(w, listWikiNodesResponse{Nodes: nodes})
	})
	mux.HandleFunc("/v2.0/wiki/nodes/", func(w http.ResponseWriter, r *http.Request) {
		nodeID := strings.TrimPrefix(r.URL.Path, "/v2.0/wiki/nodes/")
		for _, n := range nodes {
			if n.NodeID == nodeID {
				writeJSON(w, map[string]interface{}{"node": n})
				return
			}
		}
		http.NotFound(w, r)
	})
	mux.HandleFunc("/v1.0/doc/suites/documents/doc1/blocks", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("operatorId") != "operator" {
			http.Error(w, `{"message":"missing operatorId"}`, http.StatusBadRequest)
			return
		}
		writeJSON(w, map[string]interface{}{
			"blocks": []map[string]interface{}{
				{
					"type": "paragraph",
					"paragraph": map[string]interface{}{
						"elements": []map[string]interface{}{
							{"textRun": map[string]string{"text": "# Wiki\n\nDingTalk wiki body."}},
						},
					},
				},
			},
		})
	})
	mux.HandleFunc("/v1.0/doc/workbooks/sheet1/sheets", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("operatorId") != "operator" {
			http.Error(w, `{"message":"missing operatorId"}`, http.StatusBadRequest)
			return
		}
		writeJSON(w, listWorkbookSheetsResponse{
			Value: []workbookSheetItem{{
				ID:   "Sheet1",
				Name: "Sheet1",
			}},
		})
	})
	mux.HandleFunc("/v1.0/doc/workbooks/sheet1/sheets/Sheet1/ranges/A1:AX600", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("operatorId") != "operator" {
			http.Error(w, `{"message":"missing operatorId"}`, http.StatusBadRequest)
			return
		}
		writeJSON(w, map[string]interface{}{
			"values": [][]string{
				{"商品", "数量"},
				{"苹果", "3"},
			},
		})
	})
	mux.HandleFunc("/v1.0/doc/workbooks/sheet1/sheets/Sheet1/ranges/A601:AX1000", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("operatorId") != "operator" {
			http.Error(w, `{"message":"missing operatorId"}`, http.StatusBadRequest)
			return
		}
		writeJSON(w, map[string]interface{}{
			"values": [][]string{},
		})
	})

	srv := httptest.NewServer(mux)
	cfg := &Config{AppKey: "key", AppSecret: "secret", BaseURL: srv.URL}
	return srv, cfg
}

func TestConnector_Type(t *testing.T) {
	c := NewConnector()
	if c.Type() != types.ConnectorTypeDingTalk {
		t.Fatalf("type = %q", c.Type())
	}
}

func TestExtractDingTalkBlockText_convertsMarkdownTableCells(t *testing.T) {
	document := map[string]interface{}{
		"blocks": []interface{}{
			map[string]interface{}{
				"type": "paragraph",
				"paragraph": map[string]interface{}{
					"elements": []interface{}{
						map[string]interface{}{"textRun": map[string]interface{}{"text": "说明"}},
					},
				},
			},
			map[string]interface{}{
				"type": "table",
				"table": map[string]interface{}{
					"rowSize": 2,
					"colSize": 2,
					"rows": []interface{}{
						[]interface{}{
							map[string]interface{}{"value": "名称"},
							map[string]interface{}{"value": "数量"},
						},
						[]interface{}{
							map[string]interface{}{"value": "苹果"},
							map[string]interface{}{"value": "3"},
						},
					},
				},
			},
		},
	}

	got := extractDingTalkBlockText(document)
	want := "说明\n\n| 名称 | 数量 |\n| --- | --- |\n| 苹果 | 3 |"
	if got != want {
		t.Fatalf("extractDingTalkBlockText() = %q, want %q", got, want)
	}
}

func TestExtractDingTalkBlockText_readsDingTalkResultDataTableCells(t *testing.T) {
	document := map[string]interface{}{
		"result": map[string]interface{}{
			"data": []interface{}{
				map[string]interface{}{
					"blockType": "table",
					"table": map[string]interface{}{
						"cells": []interface{}{
							[]interface{}{"名称", "数量"},
							[]interface{}{"苹果", "3"},
						},
					},
				},
			},
		},
	}

	got := extractDingTalkBlockText(document)
	want := "| 名称 | 数量 |\n| --- | --- |\n| 苹果 | 3 |"
	if got != want {
		t.Fatalf("extractDingTalkBlockText() = %q, want %q", got, want)
	}
}

func TestExtractDingTalkBlockText_convertsImageTableCellsToMarkdownLinks(t *testing.T) {
	document := map[string]interface{}{
		"result": map[string]interface{}{
			"data": []interface{}{
				map[string]interface{}{
					"blockType": "table",
					"table": map[string]interface{}{
						"cells": []interface{}{
							[]interface{}{"名称", "图片"},
							[]interface{}{"商品", map[string]interface{}{
								"type":  "image",
								"image": map[string]interface{}{"url": "https://example.com/item.png"},
							}},
						},
					},
				},
			},
		},
	}

	got := extractDingTalkBlockText(document)
	want := "| 名称 | 图片 |\n| --- | --- |\n| 商品 | ![](https://example.com/item.png) |"
	if got != want {
		t.Fatalf("extractDingTalkBlockText() = %q, want %q", got, want)
	}
}

func TestParseDingTalkConfig_acceptsAppIDAlias(t *testing.T) {
	cfg, _, err := parseDingTalkConfig(&types.DataSourceConfig{
		Credentials: map[string]interface{}{
			"app_id":     "key",
			"app_secret": "secret",
			"union_id":   "operator",
		},
	})
	if err != nil {
		t.Fatalf("parseDingTalkConfig: %v", err)
	}
	if cfg.AppKey != "key" || cfg.AppSecret != "secret" {
		t.Fatalf("unexpected config: %+v", cfg)
	}
}

func TestFetchIncremental_skipsUnchanged(t *testing.T) {
	dentries := []dentryItem{{
		ID:          "doc1",
		Name:        "Spec",
		Type:        "FILE",
		Extension:   "docx",
		UpdatedTime: "2026-01-01T00:00:00Z",
	}}
	srv, cfg := fakeDingTalkServer(dentries)
	defer srv.Close()

	conn := NewConnector()
	dsConfig := &types.DataSourceConfig{
		Type: types.ConnectorTypeDingTalk,
		Credentials: map[string]interface{}{
			"app_key":    cfg.AppKey,
			"app_secret": cfg.AppSecret,
			"union_id":   "operator",
			"base_url":   cfg.BaseURL,
		},
		ResourceIDs: []string{makeSpaceResourceID("sp1")},
	}

	ctx := context.Background()
	prev := &types.SyncCursor{
		ConnectorCursor: map[string]interface{}{
			"parser_version": "3",
			"doc_revisions": map[string]string{
				makeStableDocExternalID("sp1", "doc1"): "2026-01-01T00:00:00Z",
			},
		},
	}

	items, next, err := conn.FetchIncremental(ctx, dsConfig, prev)
	if err != nil {
		t.Fatalf("FetchIncremental: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("expected 0 changed items, got %d", len(items))
	}
	if next == nil || next.ConnectorCursor == nil {
		t.Fatal("expected next cursor")
	}
}

func TestFetchIncremental_reprocessesWhenParserVersionChanges(t *testing.T) {
	dentries := []dentryItem{{
		ID:          "doc1",
		Name:        "Spec",
		Type:        "FILE",
		Extension:   "md",
		UpdatedTime: "2026-01-01T00:00:00Z",
	}}
	srv, cfg := fakeDingTalkServer(dentries)
	defer srv.Close()

	dsConfig := &types.DataSourceConfig{
		Type: types.ConnectorTypeDingTalk,
		Credentials: map[string]interface{}{
			"app_key":    cfg.AppKey,
			"app_secret": cfg.AppSecret,
			"union_id":   "operator",
			"base_url":   cfg.BaseURL,
		},
		ResourceIDs: []string{makeSpaceResourceID("sp1")},
	}
	prev := &types.SyncCursor{
		ConnectorCursor: map[string]interface{}{
			"parser_version": "old",
			"doc_revisions": map[string]string{
				makeStableDocExternalID("sp1", "doc1"): "2026-01-01T00:00:00Z",
			},
		},
	}

	items, _, err := NewConnector().FetchIncremental(context.Background(), dsConfig, prev)
	if err != nil {
		t.Fatalf("FetchIncremental: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item after parser version change, got %d", len(items))
	}
}

func TestFetchIncremental_fetchesWhenRevisionChanges(t *testing.T) {
	dentries := []dentryItem{{
		ID:          "doc1",
		Name:        "Spec",
		Type:        "FILE",
		Extension:   "md",
		UpdatedTime: "2026-06-01T00:00:00Z",
	}}
	srv, cfg := fakeDingTalkServer(dentries)
	defer srv.Close()

	conn := NewConnector()
	dsConfig := &types.DataSourceConfig{
		Type: types.ConnectorTypeDingTalk,
		Credentials: map[string]interface{}{
			"app_key":    cfg.AppKey,
			"app_secret": cfg.AppSecret,
			"union_id":   "operator",
			"base_url":   cfg.BaseURL,
		},
		ResourceIDs: []string{makeSpaceResourceID("sp1")},
		Settings: map[string]interface{}{
			"include_subfolders": true,
		},
	}

	prev := &types.SyncCursor{
		ConnectorCursor: map[string]interface{}{
			"doc_revisions": map[string]string{
				makeStableDocExternalID("sp1", "doc1"): "2026-01-01T00:00:00Z",
			},
		},
	}

	items, _, err := conn.FetchIncremental(context.Background(), dsConfig, prev)
	if err != nil {
		t.Fatalf("FetchIncremental: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if len(items[0].Content) == 0 {
		t.Fatal("expected content bytes")
	}
}

func TestFetchIncremental_driveAdocNormalizesToMarkdown(t *testing.T) {
	dentries := []dentryItem{{
		ID:          "doc1",
		Name:        "测试用.adoc",
		Type:        "FILE",
		Extension:   "adoc",
		UpdatedTime: "2026-06-01T00:00:00Z",
	}}
	srv, cfg := fakeDingTalkServer(dentries)
	defer srv.Close()

	dsConfig := &types.DataSourceConfig{
		Type: types.ConnectorTypeDingTalk,
		Credentials: map[string]interface{}{
			"app_key":    cfg.AppKey,
			"app_secret": cfg.AppSecret,
			"union_id":   "operator",
			"base_url":   cfg.BaseURL,
		},
		ResourceIDs: []string{makeSpaceResourceID("sp1")},
	}

	items, _, err := NewConnector().FetchIncremental(context.Background(), dsConfig, nil)
	if err != nil {
		t.Fatalf("FetchIncremental: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if items[0].FileName != "测试用.md" {
		t.Fatalf("FileName = %q, want 测试用.md", items[0].FileName)
	}
	if items[0].ContentType != "text/markdown" {
		t.Fatalf("ContentType = %q, want text/markdown", items[0].ContentType)
	}
}

func TestFetchIncremental_emitsDeletedItemsForMissingDocs(t *testing.T) {
	srv, cfg := fakeDingTalkServer(nil)
	defer srv.Close()

	dsConfig := &types.DataSourceConfig{
		Type: types.ConnectorTypeDingTalk,
		Credentials: map[string]interface{}{
			"app_key":    cfg.AppKey,
			"app_secret": cfg.AppSecret,
			"union_id":   "operator",
			"base_url":   cfg.BaseURL,
		},
		ResourceIDs: []string{makeSpaceResourceID("sp1")},
	}
	prev := &types.SyncCursor{
		ConnectorCursor: map[string]interface{}{
			"doc_revisions": map[string]string{
				makeStableDocExternalID("sp1", "doc1"): "2026-01-01T00:00:00Z",
			},
		},
	}

	items, next, err := NewConnector().FetchIncremental(context.Background(), dsConfig, prev)
	if err != nil {
		t.Fatalf("FetchIncremental: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 deleted item, got %d", len(items))
	}
	if !items[0].IsDeleted {
		t.Fatalf("expected deleted item, got %+v", items[0])
	}
	if items[0].ExternalID != makeStableDocExternalID("sp1", "doc1") {
		t.Fatalf("ExternalID = %q", items[0].ExternalID)
	}
	var cursor dingtalkCursor
	b, _ := json.Marshal(next.ConnectorCursor)
	if err := json.Unmarshal(b, &cursor); err != nil {
		t.Fatalf("unmarshal next cursor: %v", err)
	}
	if _, ok := cursor.DocRevisions[makeStableDocExternalID("sp1", "doc1")]; ok {
		t.Fatalf("deleted doc remained in next cursor: %+v", cursor.DocRevisions)
	}
}

func TestFetchAll_ChildDentryListErrorReturnsPartialItems(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1.0/oauth2/accessToken", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, oauthTokenResponse{AccessToken: "test-token", ExpireIn: 7200})
	})
	mux.HandleFunc("/v1.0/storage/spaces/sp1/dentries", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("parentId") {
		case "0":
			writeJSON(w, listDentriesResponse{Dentries: []dentryItem{
				{ID: "folder1", Name: "No Permission Folder", Type: "FOLDER", UpdatedTime: "2026-06-01T00:00:00Z"},
				{ID: "peer1", Name: "Peer", Type: "FILE", Extension: "md", UpdatedTime: "2026-06-01T00:00:00Z"},
			}})
		case "folder1":
			http.Error(w, `{"message":"permission denied"}`, http.StatusForbidden)
		default:
			writeJSON(w, listDentriesResponse{})
		}
	})
	mux.HandleFunc("/v1.0/doc/documents/peer1/content", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]string{"content": "# Peer\n\nDingTalk doc body."})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	dsConfig := &types.DataSourceConfig{
		Type: types.ConnectorTypeDingTalk,
		Credentials: map[string]interface{}{
			"app_key":    "key",
			"app_secret": "secret",
			"union_id":   "operator",
			"base_url":   srv.URL,
		},
		ResourceIDs: []string{makeSpaceResourceID("sp1")},
	}

	items, err := NewConnector().FetchAll(context.Background(), dsConfig, []string{makeSpaceResourceID("sp1")})
	if err != nil {
		t.Fatalf("FetchAll must not abort when one child listing fails: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("want 2 items (1 fetched + 1 failure placeholder), got %d: %+v", len(items), items)
	}

	var peer, placeholder *types.FetchedItem
	for i := range items {
		switch {
		case items[i].ExternalID == makeStableDocExternalID("sp1", "peer1") && len(items[i].Content) > 0:
			peer = &items[i]
		case items[i].Metadata["error"] != "":
			placeholder = &items[i]
		}
	}
	if peer == nil {
		t.Fatalf("expected peer document to be fetched: %+v", items)
	}
	if placeholder == nil {
		t.Fatalf("expected failure placeholder: %+v", items)
	}
	if placeholder.Title != "No Permission Folder" {
		t.Fatalf("placeholder title = %q", placeholder.Title)
	}
	if placeholder.Metadata["channel"] != types.ChannelDingtalk {
		t.Fatalf("placeholder channel = %q", placeholder.Metadata["channel"])
	}
	if placeholder.Metadata["failure_stage"] != "list_children" {
		t.Fatalf("placeholder metadata = %+v", placeholder.Metadata)
	}
}

func TestListResources_root(t *testing.T) {
	srv, cfg := fakeDingTalkServer(nil)
	defer srv.Close()

	conn := NewConnector()
	dsConfig := &types.DataSourceConfig{
		Credentials: map[string]interface{}{
			"app_key":    cfg.AppKey,
			"app_secret": cfg.AppSecret,
			"union_id":   "operator",
			"base_url":   cfg.BaseURL,
		},
	}
	res, err := conn.ListResources(context.Background(), dsConfig, "")
	if err != nil {
		t.Fatalf("ListResources: %v", err)
	}
	if len(res) != 1 || res[0].Type != "space" {
		t.Fatalf("unexpected resources: %+v", res)
	}
}

func TestListResources_wikiRootAndChildren(t *testing.T) {
	nodes := []wikiNodeItem{{
		NodeID:      "doc1",
		WorkspaceID: "wk1",
		Name:        "Wiki Spec",
		Type:        "FILE",
		Category:    "ALIDOC",
		URL:         "https://alidocs.dingtalk.com/i/nodes/doc1",
	}}
	srv, cfg := fakeDingTalkWikiServer(nodes)
	defer srv.Close()

	conn := NewConnector()
	dsConfig := &types.DataSourceConfig{
		Credentials: map[string]interface{}{
			"app_key":    cfg.AppKey,
			"app_secret": cfg.AppSecret,
			"base_url":   cfg.BaseURL,
		},
		Settings: map[string]interface{}{
			"union_id":      "operator",
			"dingtalk_type": "wiki",
		},
	}
	roots, err := conn.ListResources(context.Background(), dsConfig, "")
	if err != nil {
		t.Fatalf("ListResources root: %v", err)
	}
	if len(roots) != 1 || roots[0].ExternalID != makeWikiWorkspaceResourceID("wk1") {
		t.Fatalf("unexpected roots: %+v", roots)
	}
	children, err := conn.ListResources(context.Background(), dsConfig, roots[0].ExternalID)
	if err != nil {
		t.Fatalf("ListResources children: %v", err)
	}
	if len(children) != 1 || children[0].ExternalID != makeWikiNodeResourceID("wk1", "doc1") {
		t.Fatalf("unexpected children: %+v", children)
	}
}

func TestFetchIncremental_wikiFetchesChangedDoc(t *testing.T) {
	nodes := []wikiNodeItem{{
		NodeID:      "doc1",
		WorkspaceID: "wk1",
		Name:        "测试用.adoc",
		Type:        "FILE",
		Category:    "ALIDOC",
		URL:         "https://alidocs.dingtalk.com/i/nodes/doc1",
		UpdatedTime: "2026-06-01T00:00:00Z",
	}}
	srv, cfg := fakeDingTalkWikiServer(nodes)
	defer srv.Close()

	dsConfig := &types.DataSourceConfig{
		Type: types.ConnectorTypeDingTalk,
		Credentials: map[string]interface{}{
			"app_key":    cfg.AppKey,
			"app_secret": cfg.AppSecret,
			"base_url":   cfg.BaseURL,
		},
		ResourceIDs: []string{makeWikiWorkspaceResourceID("wk1")},
		Settings: map[string]interface{}{
			"union_id":      "operator",
			"dingtalk_type": "wiki",
		},
	}
	items, _, err := NewConnector().FetchIncremental(context.Background(), dsConfig, nil)
	if err != nil {
		t.Fatalf("FetchIncremental wiki: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if items[0].Metadata["dingtalk_doc_type"] != "wiki" {
		t.Fatalf("unexpected metadata: %+v", items[0].Metadata)
	}
	if items[0].FileName != "测试用.md" {
		t.Fatalf("FileName = %q, want 测试用.md", items[0].FileName)
	}
	if items[0].ContentType != "text/markdown" {
		t.Fatalf("ContentType = %q, want text/markdown", items[0].ContentType)
	}
	if !strings.Contains(string(items[0].Content), "DingTalk wiki body") {
		t.Fatalf("unexpected content: %s", string(items[0].Content))
	}
}

func TestFetchIncremental_wikiSpreadsheetUsesWorkbookAPI(t *testing.T) {
	nodes := []wikiNodeItem{{
		NodeID:      "sheet1",
		WorkspaceID: "wk1",
		Name:        "测试用2.xlsx",
		Type:        "FILE",
		Category:    "SPREADSHEET",
		URL:         "https://alidocs.dingtalk.com/i/nodes/sheet1",
		UpdatedTime: "2026-06-01T00:00:00Z",
	}}
	srv, cfg := fakeDingTalkWikiServer(nodes)
	defer srv.Close()

	dsConfig := &types.DataSourceConfig{
		Type: types.ConnectorTypeDingTalk,
		Credentials: map[string]interface{}{
			"app_key":    cfg.AppKey,
			"app_secret": cfg.AppSecret,
			"base_url":   cfg.BaseURL,
		},
		ResourceIDs: []string{makeWikiWorkspaceResourceID("wk1")},
		Settings: map[string]interface{}{
			"union_id":      "operator",
			"dingtalk_type": "wiki",
		},
	}
	items, _, err := NewConnector().FetchIncremental(context.Background(), dsConfig, nil)
	if err != nil {
		t.Fatalf("FetchIncremental wiki sheet: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if items[0].Metadata["error"] != "" {
		t.Fatalf("unexpected fetch error: %+v", items[0].Metadata)
	}
	if items[0].FileName != "测试用2.md" {
		t.Fatalf("FileName = %q, want 测试用2.md", items[0].FileName)
	}
	body := string(items[0].Content)
	for _, want := range []string{"# 测试用2.xlsx", "## Sheet1", "| 商品 | 数量 |", "| 苹果 | 3 |"} {
		if !strings.Contains(body, want) {
			t.Fatalf("content missing %q: %s", want, body)
		}
	}
}

func TestFetchIncremental_wikiDocumentCategoryAxlsUsesWorkbookAPI(t *testing.T) {
	nodes := []wikiNodeItem{{
		NodeID:      "sheet1",
		WorkspaceID: "wk1",
		Name:        "在线测试表.axls",
		Type:        "FILE",
		Category:    "DOCUMENT",
		URL:         "https://alidocs.dingtalk.com/i/nodes/sheet1",
		UpdatedTime: "2026-06-01T00:00:00Z",
	}}
	srv, cfg := fakeDingTalkWikiServer(nodes)
	defer srv.Close()

	dsConfig := &types.DataSourceConfig{
		Type: types.ConnectorTypeDingTalk,
		Credentials: map[string]interface{}{
			"app_key":    cfg.AppKey,
			"app_secret": cfg.AppSecret,
			"base_url":   cfg.BaseURL,
		},
		ResourceIDs: []string{makeWikiWorkspaceResourceID("wk1")},
		Settings: map[string]interface{}{
			"union_id":      "operator",
			"dingtalk_type": "wiki",
		},
	}
	items, _, err := NewConnector().FetchIncremental(context.Background(), dsConfig, nil)
	if err != nil {
		t.Fatalf("FetchIncremental wiki axls sheet: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if items[0].Metadata["error"] != "" {
		t.Fatalf("unexpected fetch error: %+v", items[0].Metadata)
	}
	if items[0].Metadata["dingtalk_doc_kind"] != string(dingtalkDocumentKindNativeSheet) {
		t.Fatalf("doc kind = %q, want native sheet", items[0].Metadata["dingtalk_doc_kind"])
	}
	if !strings.Contains(string(items[0].Content), "| 商品 | 数量 |") {
		t.Fatalf("expected sheet markdown content, got %s", string(items[0].Content))
	}
}

func TestFetchIncremental_wikiUnsupportedOnlineCreationIsSkipped(t *testing.T) {
	nodes := []wikiNodeItem{{
		NodeID:      "whiteboard1",
		WorkspaceID: "wk1",
		Name:        "讨论白板",
		Type:        "FILE",
		Category:    "WHITEBOARD",
		UpdatedTime: "2026-06-01T00:00:00Z",
	}}
	srv, cfg := fakeDingTalkWikiServer(nodes)
	defer srv.Close()

	dsConfig := &types.DataSourceConfig{
		Type: types.ConnectorTypeDingTalk,
		Credentials: map[string]interface{}{
			"app_key":    cfg.AppKey,
			"app_secret": cfg.AppSecret,
			"base_url":   cfg.BaseURL,
		},
		ResourceIDs: []string{makeWikiWorkspaceResourceID("wk1")},
		Settings: map[string]interface{}{
			"union_id":      "operator",
			"dingtalk_type": "wiki",
		},
	}
	items, _, err := NewConnector().FetchIncremental(context.Background(), dsConfig, nil)
	if err != nil {
		t.Fatalf("FetchIncremental unsupported online creation: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 skipped item, got %d", len(items))
	}
	if items[0].Metadata["error"] != "" || items[0].Metadata["skip_reason"] == "" {
		t.Fatalf("expected skip placeholder, got %+v", items[0].Metadata)
	}
	if !strings.Contains(items[0].Metadata["skip_reason"], "暂不支持同步钉钉白板") {
		t.Fatalf("unexpected skip reason: %s", items[0].Metadata["skip_reason"])
	}
}

func TestFetchIncremental_failedItemDoesNotAdvanceCursor(t *testing.T) {
	nodes := []wikiNodeItem{{
		NodeID:      "missing",
		WorkspaceID: "wk1",
		Name:        "坏文档.adoc",
		Type:        "FILE",
		Category:    "ALIDOC",
		UpdatedTime: "2026-06-01T00:00:00Z",
	}}
	srv, cfg := fakeDingTalkWikiServer(nodes)
	defer srv.Close()

	dsConfig := &types.DataSourceConfig{
		Type: types.ConnectorTypeDingTalk,
		Credentials: map[string]interface{}{
			"app_key":    cfg.AppKey,
			"app_secret": cfg.AppSecret,
			"base_url":   cfg.BaseURL,
		},
		ResourceIDs: []string{makeWikiWorkspaceResourceID("wk1")},
		Settings: map[string]interface{}{
			"union_id":      "operator",
			"dingtalk_type": "wiki",
		},
	}
	items, next, err := NewConnector().FetchIncremental(context.Background(), dsConfig, nil)
	if err != nil {
		t.Fatalf("FetchIncremental failed item: %v", err)
	}
	if len(items) != 1 || items[0].Metadata["error"] == "" {
		t.Fatalf("expected one failed item, got %+v", items)
	}
	var cursor dingtalkCursor
	b, _ := json.Marshal(next.ConnectorCursor)
	if err := json.Unmarshal(b, &cursor); err != nil {
		t.Fatalf("unmarshal next cursor: %v", err)
	}
	if _, ok := cursor.DocRevisions[makeStableWikiDocExternalID("wk1", "missing")]; ok {
		t.Fatalf("failed item advanced cursor: %+v", cursor.DocRevisions)
	}
}

func TestFetchIncremental_wikiUploadedSpreadsheetIsSkipped(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1.0/oauth2/accessToken", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, oauthTokenResponse{AccessToken: "test-token", ExpireIn: 7200})
	})
	mux.HandleFunc("/v2.0/wiki/workspaces", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, listWikiWorkspacesResponse{
			Workspaces: []wikiWorkspaceItem{{WorkspaceID: "wk1", Name: "Knowledge", RootNodeID: "root1"}},
		})
	})
	mux.HandleFunc("/v2.0/wiki/nodes", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, listWikiNodesResponse{Nodes: []wikiNodeItem{{
			NodeID:      "file1",
			WorkspaceID: "wk1",
			Name:        "上传表格.xlsx",
			Type:        "FILE",
			Category:    "DOCUMENT",
			UpdatedTime: "2026-06-01T00:00:00Z",
		}}})
	})
	mux.HandleFunc("/v2.0/wiki/nodes/file1", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]interface{}{"node": wikiNodeItem{
			NodeID:      "file1",
			WorkspaceID: "wk1",
			Name:        "上传表格.xlsx",
			Type:        "FILE",
			Category:    "DOCUMENT",
			SpaceID:     "drive-sp1",
			DentryID:    "drive-file1",
			UpdatedTime: "2026-06-01T00:00:00Z",
		}})
	})
	mux.HandleFunc("/v1.0/doc/workbooks/file1/sheets", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"not workbook"}`, http.StatusBadRequest)
	})
	mux.HandleFunc("/v1.0/storage/spaces/drive-sp1/dentries/drive-file1/downloadInfos/query", func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("uploaded spreadsheet should be skipped without requesting download info")
	})
	mux.HandleFunc("/v1.0/storage/spaces/drive-sp1/dentries/drive-file1/downloadInfos", func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("uploaded spreadsheet should be skipped without requesting legacy download info")
	})
	mux.HandleFunc("/download/file1", func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("uploaded spreadsheet should be skipped without downloading bytes")
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	dsConfig := &types.DataSourceConfig{
		Type: types.ConnectorTypeDingTalk,
		Credentials: map[string]interface{}{
			"app_key":    "key",
			"app_secret": "secret",
			"base_url":   srv.URL,
		},
		ResourceIDs: []string{makeWikiWorkspaceResourceID("wk1")},
		Settings: map[string]interface{}{
			"union_id":      "operator",
			"dingtalk_type": "wiki",
		},
	}
	items, _, err := NewConnector().FetchIncremental(context.Background(), dsConfig, nil)
	if err != nil {
		t.Fatalf("FetchIncremental wiki uploaded sheet: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if items[0].Metadata["error"] != "" || items[0].Metadata["skip_reason"] == "" {
		t.Fatalf("expected skip placeholder, got %+v", items[0].Metadata)
	}
	if len(items[0].Content) != 0 || items[0].FileName != "" {
		t.Fatalf("skipped uploaded file should not contain bytes or filename, got file=%q len=%d", items[0].FileName, len(items[0].Content))
	}
	if !strings.Contains(items[0].Metadata["skip_reason"], "转换为钉钉在线文档或在线表格") {
		t.Fatalf("unexpected skip reason: %s", items[0].Metadata["skip_reason"])
	}
}

func TestFetchIncremental_wikiDocumentXlsxWithoutLocatorIsSkipped(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1.0/oauth2/accessToken", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, oauthTokenResponse{AccessToken: "test-token", ExpireIn: 7200})
	})
	mux.HandleFunc("/v2.0/wiki/workspaces", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, listWikiWorkspacesResponse{
			Workspaces: []wikiWorkspaceItem{{WorkspaceID: "wk1", Name: "Knowledge", RootNodeID: "root1"}},
		})
	})
	mux.HandleFunc("/v2.0/wiki/nodes", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, listWikiNodesResponse{Nodes: []wikiNodeItem{{
			NodeID:      "file1",
			WorkspaceID: "wk1",
			Name:        "上传表格.xlsx",
			Type:        "FILE",
			Category:    "DOCUMENT",
			UpdatedTime: "2026-06-01T00:00:00Z",
		}}})
	})
	mux.HandleFunc("/v2.0/wiki/nodes/file1", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]interface{}{"node": wikiNodeItem{
			NodeID:      "file1",
			WorkspaceID: "wk1",
			Name:        "上传表格.xlsx",
			Type:        "FILE",
			Category:    "DOCUMENT",
			UpdatedTime: "2026-06-01T00:00:00Z",
		}})
	})
	mux.HandleFunc("/v1.0/doc/workbooks/file1/sheets", func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("DOCUMENT .xlsx without workbook metadata should be skipped before workbook API")
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	dsConfig := &types.DataSourceConfig{
		Type: types.ConnectorTypeDingTalk,
		Credentials: map[string]interface{}{
			"app_key":    "key",
			"app_secret": "secret",
			"base_url":   srv.URL,
		},
		ResourceIDs: []string{makeWikiWorkspaceResourceID("wk1")},
		Settings: map[string]interface{}{
			"union_id":      "operator",
			"dingtalk_type": "wiki",
		},
	}
	items, _, err := NewConnector().FetchIncremental(context.Background(), dsConfig, nil)
	if err != nil {
		t.Fatalf("FetchIncremental wiki uploaded-like sheet: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 skipped item, got %d", len(items))
	}
	if items[0].Metadata["skip_reason"] == "" {
		t.Fatalf("expected skip reason, got %+v", items[0].Metadata)
	}
	if items[0].Metadata["dingtalk_doc_kind"] != string(dingtalkDocumentKindUploadedFile) {
		t.Fatalf("doc kind = %q, want uploaded file", items[0].Metadata["dingtalk_doc_kind"])
	}
}

func TestNormalizeWikiDocumentKind_DocumentOfficeTextFilesAreUploadedFiles(t *testing.T) {
	for _, name := range []string{"测试文档_纯文本.txt", "测试报告_Word文档.docx", "README_测试说明.md"} {
		got := normalizeWikiDocumentKind(wikiNodeItem{
			NodeID:   "file1",
			Name:     name,
			Type:     "FILE",
			Category: "DOCUMENT",
		})
		if got != dingtalkDocumentKindUploadedFile {
			t.Fatalf("normalizeWikiDocumentKind(%q) = %q, want %q", name, got, dingtalkDocumentKindUploadedFile)
		}
	}
}

func TestNormalizeWikiDocumentKind_DocumentNativeDocStaysNative(t *testing.T) {
	for _, name := range []string{"在线文档.adoc", "在线文档"} {
		got := normalizeWikiDocumentKind(wikiNodeItem{
			NodeID:   "doc1",
			Name:     name,
			Type:     "FILE",
			Category: "DOCUMENT",
		})
		if got != dingtalkDocumentKindNativeDoc {
			t.Fatalf("normalizeWikiDocumentKind(%q) = %q, want %q", name, got, dingtalkDocumentKindNativeDoc)
		}
	}
}

func TestSpreadsheetRangeAddressesRespectDingTalkCellLimit(t *testing.T) {
	got := spreadsheetRangeAddresses(0, 0)
	want := []string{"A1:AX600", "A601:AX1000"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("spreadsheetRangeAddresses(0,0) = %#v, want %#v", got, want)
	}
	for _, r := range spreadsheetRangeAddresses(1000, 50) {
		parts := strings.Split(r, ":")
		if len(parts) != 2 {
			t.Fatalf("invalid range %q", r)
		}
		startRow := rowNumberFromCell(parts[0])
		endRow := rowNumberFromCell(parts[1])
		cells := (endRow - startRow + 1) * 50
		if cells > dingtalkSpreadsheetMaxCellsPerRange {
			t.Fatalf("range %q has %d cells, exceeds %d", r, cells, dingtalkSpreadsheetMaxCellsPerRange)
		}
	}
}

func rowNumberFromCell(cell string) int {
	row := 0
	for _, ch := range cell {
		if ch >= '0' && ch <= '9' {
			row = row*10 + int(ch-'0')
		}
	}
	return row
}

func TestFetchIncremental_wikiUploadedFileIsSkippedWithoutResolvingDentry(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1.0/oauth2/accessToken", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, oauthTokenResponse{AccessToken: "test-token", ExpireIn: 7200})
	})
	mux.HandleFunc("/v2.0/wiki/workspaces", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, listWikiWorkspacesResponse{
			Workspaces: []wikiWorkspaceItem{{WorkspaceID: "wk1", Name: "Knowledge", RootNodeID: "root1"}},
		})
	})
	mux.HandleFunc("/v2.0/wiki/nodes", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, listWikiNodesResponse{Nodes: []wikiNodeItem{{
			NodeID:      "wiki-dentry-uuid",
			WorkspaceID: "wk1",
			Name:        "制度.pdf",
			Type:        "FILE",
			Category:    "DOCUMENT",
			UpdatedTime: "2026-06-01T00:00:00Z",
		}}})
	})
	mux.HandleFunc("/v2.0/wiki/nodes/wiki-dentry-uuid", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]interface{}{"node": wikiNodeItem{
			NodeID:      "wiki-dentry-uuid",
			WorkspaceID: "wk1",
			Name:        "制度.pdf",
			Type:        "FILE",
			Category:    "DOCUMENT",
			UpdatedTime: "2026-06-01T00:00:00Z",
		}})
	})
	mux.HandleFunc("/v2.0/doc/dentries/wiki-dentry-uuid/queryDentryId", func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("uploaded file should be skipped without resolving dentry id")
	})
	mux.HandleFunc("/v1.0/storage/spaces/drive-sp1/dentries/drive-file1/downloadInfos/query", func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("uploaded file should be skipped without requesting download info")
	})
	mux.HandleFunc("/download/file1", func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("uploaded file should be skipped without downloading bytes")
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	dsConfig := &types.DataSourceConfig{
		Type: types.ConnectorTypeDingTalk,
		Credentials: map[string]interface{}{
			"app_key":    "key",
			"app_secret": "secret",
			"base_url":   srv.URL,
		},
		ResourceIDs: []string{makeWikiWorkspaceResourceID("wk1")},
		Settings: map[string]interface{}{
			"union_id":      "operator",
			"dingtalk_type": "wiki",
		},
	}
	items, _, err := NewConnector().FetchIncremental(context.Background(), dsConfig, nil)
	if err != nil {
		t.Fatalf("FetchIncremental wiki uploaded file: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if items[0].Metadata["error"] != "" || items[0].Metadata["skip_reason"] == "" {
		t.Fatalf("expected skip placeholder, got %+v", items[0].Metadata)
	}
}

func TestFetchIncremental_driveUploadedFileIsSkipped(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1.0/oauth2/accessToken", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, oauthTokenResponse{AccessToken: "test-token", ExpireIn: 7200})
	})
	mux.HandleFunc("/v1.0/storage/spaces/sp1/dentries", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, listDentriesResponse{Dentries: []dentryItem{{
			ID:          "file1",
			Name:        "上传文件.pdf",
			Type:        "FILE",
			Extension:   "pdf",
			UpdatedTime: "2026-06-01T00:00:00Z",
		}}})
	})
	mux.HandleFunc("/v1.0/storage/spaces/sp1/dentries/file1/downloadInfos/query", func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("uploaded drive file should be skipped without requesting download info")
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	dsConfig := &types.DataSourceConfig{
		Type: types.ConnectorTypeDingTalk,
		Credentials: map[string]interface{}{
			"app_key":    "key",
			"app_secret": "secret",
			"union_id":   "operator",
			"base_url":   srv.URL,
		},
		ResourceIDs: []string{makeSpaceResourceID("sp1")},
	}
	items, _, err := NewConnector().FetchIncremental(context.Background(), dsConfig, nil)
	if err != nil {
		t.Fatalf("FetchIncremental drive uploaded file: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if items[0].Metadata["error"] != "" || items[0].Metadata["skip_reason"] == "" {
		t.Fatalf("expected skip placeholder, got %+v", items[0].Metadata)
	}
}

func TestHumanizeDingTalkAPIError_WorkbookPermission(t *testing.T) {
	got := humanizeDingTalkAPIError(
		"/v1.0/doc/workbooks/sheet1/sheets",
		"forbidden",
		http.StatusForbidden,
	)
	for _, want := range []string{"读取钉钉知识库表格工作表失败", "钉钉表格/工作簿读取权限", "UnionID"} {
		if !strings.Contains(got, want) {
			t.Fatalf("error message missing %q: %s", want, got)
		}
	}
}

func TestParseUpdatedTime(t *testing.T) {
	ts := parseUpdatedTime("2026-07-06T12:00:00Z")
	if ts.IsZero() {
		t.Fatal("expected parsed time")
	}
	if ts.Year() != 2026 {
		t.Fatalf("year=%d", ts.Year())
	}
	_ = time.UTC
}
