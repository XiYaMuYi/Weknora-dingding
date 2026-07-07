package types

import "strings"

// NormalizeQueryIntent maps LLM output (English enums, Chinese labels, typos) to a
// canonical QueryIntent. Unknown values fall back to IntentKBSearch so retrieval
// still runs — aligned with rewrite prompt: "when unsure, always choose kb_search".
func NormalizeQueryIntent(raw string) QueryIntent {
	s := normalizeIntentKey(raw)
	if s == "" {
		return ""
	}

	if intent, ok := canonicalIntentByKey[s]; ok {
		return intent
	}
	return IntentKBSearch
}

func normalizeIntentKey(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	s = strings.Trim(s, `"'`)
	s = strings.ToLower(s)
	s = strings.ReplaceAll(s, "-", "_")
	s = strings.ReplaceAll(s, " ", "_")
	return s
}

// canonicalIntentByKey maps normalized keys (lowercase, underscores) to intents.
var canonicalIntentByKey = func() map[string]QueryIntent {
	m := make(map[string]QueryIntent, 128)

	add := func(intent QueryIntent, keys ...string) {
		for _, k := range keys {
			m[normalizeIntentKey(k)] = intent
		}
	}

	add(IntentKBSearch,
		string(IntentKBSearch),
		"kb", "knowledge_base", "knowledge", "search", "retrieve", "retrieval",
		"知识库", "知识库检索", "知识库搜索", "检索", "查询", "搜索", "查找", "查资料",
		"咨询", "问答", "问知识库", "文档检索",
	)
	add(IntentWebSearch,
		string(IntentWebSearch),
		"web", "internet", "online",
		"联网", "网络搜索", "网页搜索", "在线搜索", "实时搜索", "外网",
	)
	add(IntentGreeting,
		string(IntentGreeting),
		"hello", "hi", "thanks", "thank_you",
		"问候", "打招呼", "问好", "感谢", "谢谢", "再见", "寒暄",
	)
	add(IntentChitchat,
		string(IntentChitchat),
		"chat", "small_talk", "casual",
		"闲聊", "唠嗑", "聊天", "闲谈", "废话", "玩笑",
	)
	add(IntentFollowUp,
		string(IntentFollowUp),
		"followup", "follow", "continuation",
		"追问", "跟进", "接着问", "继续问", "上文", "刚才", "展开讲讲",
	)
	add(IntentImageOnly,
		string(IntentImageOnly),
		"image", "picture", "photo", "vision", "ocr",
		"纯图片", "仅图片", "图片", "图像", "识图", "看图", "图片分析", "图",
	)
	add(IntentDocOnly,
		string(IntentDocOnly),
		"document", "file", "attachment", "doc",
		"纯文档", "仅文档", "文档", "附件", "文件", "本文档", "这份文件",
	)
	add(IntentSummarize,
		string(IntentSummarize),
		"summary", "recap", "review_dialogue",
		"总结", "概括", "归纳", "回顾对话", "总结对话", "总结一下",
	)
	add(IntentClarification,
		string(IntentClarification),
		"clarify", "ambiguous", "disambiguate",
		"澄清", "消歧", "不明确", "补充说明", "需要澄清",
	)

	return m
}()

// IsKnownQueryIntent reports whether raw (after normalization) is a registered alias.
func IsKnownQueryIntent(raw string) bool {
	s := normalizeIntentKey(raw)
	if s == "" {
		return false
	}
	_, ok := canonicalIntentByKey[s]
	return ok
}