package redisqueue

import coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"

// usageCacheInputMode describes the raw input counter using validated accounting.
// Panels must not infer a plugin's wire protocol from its provider or model name.
func usageCacheInputMode(detail coreusage.Detail) string {
	breakdown := detail.TokenBreakdown
	if !breakdown.Valid() || breakdown.Quality != coreusage.TokenAccountingQualityComplete {
		return ""
	}
	input := breakdown.Input
	if detail.CacheReadTokens != input.CacheReadTokens || detail.CacheCreationTokens != input.CacheWriteTokens {
		return ""
	}
	switch detail.InputTokens {
	case input.TotalTokens:
		return "included_in_input"
	case input.UncachedTokens:
		return "separate_from_input"
	case input.TotalTokens - input.CacheWriteTokens:
		return "read_included_creation_separate"
	default:
		return ""
	}
}
