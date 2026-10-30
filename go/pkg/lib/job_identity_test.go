package lib

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestGenerateJobNameIdentity(t *testing.T) {
	for _, tc := range []struct{ identity, prefix string }{
		{"requestor", "requestor-"},
		{"example.two@cloud.com", "example-two-"},
		{"  Requestor_Name! ", "requestor-name-"},
	} {
		name := GenerateJobName(tc.identity, 8)
		if !strings.HasPrefix(name, tc.prefix) || len(name) != len(tc.prefix)+8 {
			t.Errorf("unexpected job name %q for %q", name, tc.identity)
		}
		if !regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`).MatchString(name) {
			t.Errorf("invalid Kubernetes job name %q", name)
		}
	}
}

func TestRequestorAgreements(t *testing.T) {
	body, err := os.ReadFile(`../../../configuration/etcd_launch_files/agreements.json`)
	if err != nil {
		t.Fatal(err)
	}
	var agreements []struct {
		Name      string `json:"name"`
		Relations map[string]struct {
			ID string `json:"ID"`
		} `json:"relations"`
	}
	if err := json.Unmarshal(body, &agreements); err != nil {
		t.Fatal(err)
	}
	if len(agreements) != 6 {
		t.Fatalf("expected six encrypted-strategy providers, got %d", len(agreements))
	}
	for _, agreement := range agreements {
		relation, ok := agreement.Relations["requestor"]
		if !ok || relation.ID != "GUID" || len(agreement.Relations) != 1 {
			t.Errorf("%s: inconsistent requestor agreement", agreement.Name)
		}
	}
}
