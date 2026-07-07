package dingtalk

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
)

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func fakeDingTalkServer(dentries []dentryItem) (*httptest.Server, *Config) {
	mux := http.NewServeMux()

	mux.HandleFunc("/v1.0/oauth2/accessToken", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, oauthTokenResponse{AccessToken: "test-token", ExpireIn: 7200})
	})

	mux.HandleFunc("/v1.0/storage/spaces", func(w http.ResponseWriter, r *http.Request) {
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

func TestConnector_Type(t *testing.T) {
	c := NewConnector()
	if c.Type() != types.ConnectorTypeDingTalk {
		t.Fatalf("type = %q", c.Type())
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
			"base_url":   cfg.BaseURL,
		},
		ResourceIDs: []string{makeSpaceResourceID("sp1")},
	}

	ctx := context.Background()
	prev := &types.SyncCursor{
		ConnectorCursor: map[string]interface{}{
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

func TestListResources_root(t *testing.T) {
	srv, cfg := fakeDingTalkServer(nil)
	defer srv.Close()

	conn := NewConnector()
	dsConfig := &types.DataSourceConfig{
		Credentials: map[string]interface{}{
			"app_key":    cfg.AppKey,
			"app_secret": cfg.AppSecret,
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