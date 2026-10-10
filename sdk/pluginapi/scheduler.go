package pluginapi

import (
	"fmt"
	"strings"
)

// ValidateSchedulerCandidateFilter rejects malformed permission filters rather
// than allowing a caller to fall back to the unrestricted credential pool.
func ValidateSchedulerCandidateFilter(resp SchedulerPickResponse, candidates []SchedulerAuthCandidate) error {
	if resp.AllowedAuthIDs == nil {
		return nil
	}
	if !resp.Handled || strings.TrimSpace(resp.AuthID) != "" || strings.TrimSpace(resp.DelegateBuiltin) != "" {
		return fmt.Errorf("candidate filter requires handled selection without auth id or delegate")
	}
	known := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		known[candidate.ID] = struct{}{}
	}
	for _, id := range resp.AllowedAuthIDs {
		id = strings.TrimSpace(id)
		if id == "" {
			return fmt.Errorf("candidate filter contains an empty auth id")
		}
		if _, ok := known[id]; !ok {
			return fmt.Errorf("candidate filter contains an unknown auth id")
		}
	}
	return nil
}
