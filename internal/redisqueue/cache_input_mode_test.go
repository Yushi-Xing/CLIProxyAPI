package redisqueue

import (
	"context"
	"encoding/json"
	"testing"

	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

func TestUsageQueueCacheInputModeUsesAccountingInsteadOfModelName(t *testing.T) {
	cases := []struct {
		name      string
		input     int64
		breakdown coreusage.TokenBreakdown
		want      string
	}{
		{"zed-openai-chat", 2573, coreusage.NewSubsetTokenBreakdown(2573, 2552, 17, 29, 0, 2602), "included_in_input"},
		{"zed-native-messages", 4, coreusage.NewIndependentTokenBreakdown(4, 2552, 17, 29, 0, 2602), "separate_from_input"},
		{"read-included-write-separate", 2556, coreusage.NewSubsetTokenBreakdown(2573, 2552, 17, 29, 0, 2602), "read_included_creation_separate"},
		{"unclassified", 2573, coreusage.NewUnclassifiedTokenBreakdown(2602), ""},
		{"inconsistent", 2573, coreusage.NewSubsetTokenBreakdown(2573, 2552, 17, 29, 0, 7), ""},
		{"mismatched-raw-counter", 8000, coreusage.NewSubsetTokenBreakdown(2573, 2552, 17, 29, 0, 2602), ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withEnabledQueue(t, func() {
				detail := coreusage.Detail{InputTokens: tc.input, OutputTokens: 29, CacheReadTokens: 2552, CachedTokens: 2552, CacheCreationTokens: 17, TotalTokens: 2602, TokenBreakdown: tc.breakdown}
				(&usageQueuePlugin{}).HandleUsage(context.Background(), coreusage.Record{Provider: "zed", ExecutorType: "executorAdapter", Model: "claude-haiku-5-5", Detail: detail})
				payload := popSinglePayload(t)
				var tokens map[string]json.RawMessage
				if err := json.Unmarshal(payload["tokens"], &tokens); err != nil {
					t.Fatal(err)
				}
				if tc.want == "" {
					requireMissingField(t, tokens, "cache_input_mode")
				} else {
					requireStringField(t, tokens, "cache_input_mode", tc.want)
				}
				requireIntField(t, tokens, "input_tokens", tc.input)
				requireIntField(t, tokens, "cache_read_tokens", 2552)
				requireIntField(t, tokens, "cache_creation_tokens", 17)
				requireIntField(t, tokens, "total_tokens", 2602)
			})
		})
	}
}
