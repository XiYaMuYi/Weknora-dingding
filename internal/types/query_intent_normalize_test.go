package types

import "testing"

func TestNormalizeQueryIntent_EnglishCanonical(t *testing.T) {
	cases := []struct {
		in   string
		want QueryIntent
	}{
		{"kb_search", IntentKBSearch},
		{"KB_SEARCH", IntentKBSearch},
		{"greeting", IntentGreeting},
		{"follow_up", IntentFollowUp},
		{"follow-up", IntentFollowUp},
		{"image_only", IntentImageOnly},
	}
	for _, tc := range cases {
		if got := NormalizeQueryIntent(tc.in); got != tc.want {
			t.Errorf("NormalizeQueryIntent(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestNormalizeQueryIntent_ChineseAliases(t *testing.T) {
	cases := []struct {
		in   string
		want QueryIntent
	}{
		{"咨询", IntentKBSearch},
		{"知识库检索", IntentKBSearch},
		{"检索", IntentKBSearch},
		{"问候", IntentGreeting},
		{"闲聊", IntentChitchat},
		{"追问", IntentFollowUp},
		{"纯图片", IntentImageOnly},
		{"纯文档", IntentDocOnly},
		{"总结", IntentSummarize},
		{"澄清", IntentClarification},
		{"联网", IntentWebSearch},
	}
	for _, tc := range cases {
		if got := NormalizeQueryIntent(tc.in); got != tc.want {
			t.Errorf("NormalizeQueryIntent(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestNormalizeQueryIntent_UnknownFallsBackToKBSearch(t *testing.T) {
	unknown := []string{"unknown_intent", "随便", "other", "  未知意图  "}
	for _, in := range unknown {
		if got := NormalizeQueryIntent(in); got != IntentKBSearch {
			t.Errorf("NormalizeQueryIntent(%q) = %q, want kb_search fallback", in, got)
		}
	}
}

func TestNormalizeQueryIntent_Empty(t *testing.T) {
	if got := NormalizeQueryIntent(""); got != "" {
		t.Errorf("empty intent should stay empty, got %q", got)
	}
	if got := NormalizeQueryIntent("   "); got != "" {
		t.Errorf("whitespace intent should stay empty, got %q", got)
	}
}

func TestNeedsKBRetrieval_WithNormalizedIntents(t *testing.T) {
	if !IntentKBSearch.NeedsKBRetrieval() {
		t.Fatal("kb_search should need retrieval")
	}
	if NormalizeQueryIntent("咨询").NeedsKBRetrieval() {
		// ok
	} else {
		t.Fatal("normalized 咨询 -> kb_search should need retrieval")
	}
	if NormalizeQueryIntent("闲聊").NeedsKBRetrieval() {
		t.Fatal("chitchat should not need KB retrieval")
	}
}