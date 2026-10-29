package main

import "testing"

func TestGetMicroserviceTag(t *testing.T) {
	tests := []struct {
		name     string
		specific string
		common   string
		want     string
	}{
		{name: "default", want: "latest"},
		{name: "branch", common: "baseline", want: "baseline"},
		{name: "specific override", specific: "custom", common: "baseline", want: "custom"},
	}
	for _, service := range []string{"vfl-train", "sidecar"} {
		for _, tt := range tests {
			t.Run(service+"/"+tt.name, func(t *testing.T) {
				t.Setenv("VFL-TRAIN_TAG", tt.specific)
				t.Setenv("SIDECAR_TAG", tt.specific)
				t.Setenv("MICROSERVICE_TAG", tt.common)
				if got := getMicroserviceTag(service); got != tt.want {
					t.Fatalf("getMicroserviceTag() = %q, want %q", got, tt.want)
				}
			})
		}
	}
}
