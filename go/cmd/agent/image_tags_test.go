package main

import "testing"

func TestMicroserviceTag(t *testing.T) {
	for _, key := range []string{"VFL_TRAIN_TAG", "VFL-TRAIN_TAG", "MICROSERVICE_TAG", "SIDECAR_TAG"} {
		t.Setenv(key, "")
	}
	if got := getMicroserviceTag("vfl-train"); got != "fed-bcd" {
		t.Fatalf("default tag = %q", got)
	}
	t.Setenv("MICROSERVICE_TAG", "shared")
	if got := getMicroserviceTag("sidecar"); got != "shared" {
		t.Fatalf("shared tag = %q", got)
	}
	t.Setenv("VFL-TRAIN_TAG", "legacy")
	if got := getMicroserviceTag("vfl-train"); got != "legacy" {
		t.Fatalf("legacy override = %q", got)
	}
	t.Setenv("VFL_TRAIN_TAG", "service")
	if got := getMicroserviceTag("vfl-train"); got != "service" {
		t.Fatalf("service override = %q", got)
	}
}
