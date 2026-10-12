package dingtalk

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Tencent/WeKnora/internal/types"
)

// parseDingTalkConfig parses credentials and non-secret settings from a data source config.
func parseDingTalkConfig(config *types.DataSourceConfig) (*Config, Settings, error) {
	if config == nil {
		return nil, Settings{}, fmt.Errorf("config is nil")
	}

	var cfg Config
	if len(config.Credentials) > 0 {
		credBytes, err := json.Marshal(config.Credentials)
		if err != nil {
			return nil, Settings{}, fmt.Errorf("marshal credentials: %w", err)
		}
		if err := json.Unmarshal(credBytes, &cfg); err != nil {
			return nil, Settings{}, fmt.Errorf("parse dingtalk credentials: %w", err)
		}
	}
	if cfg.AppKey == "" {
		cfg.AppKey = stringFromMap(config.Credentials, "client_id")
	}
	if cfg.AppKey == "" {
		cfg.AppKey = stringFromMap(config.Credentials, "app_id")
	}
	if cfg.AppSecret == "" {
		cfg.AppSecret = stringFromMap(config.Credentials, "client_secret")
	}
	if cfg.AppKey == "" || cfg.AppSecret == "" {
		return nil, Settings{}, fmt.Errorf("dingtalk app_key and app_secret are required")
	}
	settings := parseSettings(config.Settings)
	if settings.DingTalkType == "" || settings.DingTalkType == "drive" {
		if v := stringFromMap(config.Credentials, "dingtalk_type"); v == "wiki" {
			settings.DingTalkType = "wiki"
		}
	}
	if cfg.OperatorUnionID == "" {
		cfg.OperatorUnionID = settings.OperatorUnionID
	}
	if cfg.OperatorUnionID == "" {
		cfg.OperatorUnionID = stringFromMap(config.Credentials, "union_id")
	}
	if cfg.OperatorUnionID == "" {
		cfg.OperatorUnionID = stringFromMap(config.Credentials, "operator_union_id")
	}
	if cfg.OperatorUnionID == "" {
		cfg.OperatorUnionID = stringFromMap(config.Credentials, "unionId")
	}
	if cfg.OperatorUnionID == "" {
		cfg.OperatorUnionID = cfg.CorpID
	}
	if cfg.OperatorUnionID == "" {
		return nil, Settings{}, fmt.Errorf("dingtalk operator union_id is required")
	}
	settings.OperatorUnionID = cfg.OperatorUnionID

	return &cfg, settings, nil
}

func stringFromMap(m map[string]interface{}, key string) string {
	if m == nil {
		return ""
	}
	v, ok := m[key]
	if !ok || v == nil {
		return ""
	}
	s, _ := v.(string)
	return strings.TrimSpace(s)
}
