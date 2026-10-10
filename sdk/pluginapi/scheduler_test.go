package pluginapi

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestSchedulerCandidateFilterJSONCompatibility(t *testing.T) {
	for _, payload := range []string{`{"Handled":true,"AllowedAuthIDs":["a","b"]}`, `{"handled":true,"allowed_auth_ids":["a","b"]}`} {
		var response SchedulerPickResponse
		if err := json.Unmarshal([]byte(payload), &response); err != nil {
			t.Fatal(err)
		}
		if !response.Handled || !reflect.DeepEqual(response.AllowedAuthIDs, []string{"a", "b"}) {
			t.Fatalf("response=%+v", response)
		}
	}
	for _, test := range []struct {
		payload string
		want    []string
	}{
		{`{"Handled":true,"AllowedAuthIDs":[]}`, []string{}},
		{`{"Handled":true,"AllowedAuthIDs":null}`, nil},
		{`{"Handled":true}`, nil},
		{`{"Handled":true,"AllowedAuthIDs":[],"allowed_auth_ids":["a"]}`, []string{}},
	} {
		var response SchedulerPickResponse
		if err := json.Unmarshal([]byte(test.payload), &response); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(response.AllowedAuthIDs, test.want) {
			t.Fatalf("%s: ids=%v, want %v", test.payload, response.AllowedAuthIDs, test.want)
		}
		encoded, err := json.Marshal(response)
		if err != nil {
			t.Fatal(err)
		}
		var decoded SchedulerPickResponse
		if err = json.Unmarshal(encoded, &decoded); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(decoded.AllowedAuthIDs, test.want) {
			t.Fatal("round trip lost deny-all distinction")
		}
	}
	for _, payload := range []string{`{"AllowedAuthIDs":"a"}`, `{"allowed_auth_ids":[1]}`} {
		var response SchedulerPickResponse
		if err := json.Unmarshal([]byte(payload), &response); err == nil {
			t.Fatalf("accepted %s", payload)
		}
	}
	response := SchedulerPickResponse{AllowedAuthIDs: []string{"a"}}
	if err := json.Unmarshal([]byte(`{"AllowedAuthIDs":[]}`), &response); err != nil || response.AllowedAuthIDs == nil || len(response.AllowedAuthIDs) != 0 {
		t.Fatalf("empty override=%+v, err=%v", response, err)
	}
	if err := json.Unmarshal([]byte(`{"AllowedAuthIDs":null}`), &response); err != nil || response.AllowedAuthIDs != nil {
		t.Fatalf("null override=%+v, err=%v", response, err)
	}
}
