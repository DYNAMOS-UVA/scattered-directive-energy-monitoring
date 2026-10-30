package main

import "testing"

func TestMicroserviceTag(t *testing.T) {
	for _, name := range []string{"vfl-party", "vfl-aggregator", "vfl-authority", "sidecar"} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("MICROSERVICE_TAG", "")
			key := ""
			switch name {
			case "vfl-party":
				key = "VFL-PARTY_TAG"
			case "vfl-aggregator":
				key = "VFL-AGGREGATOR_TAG"
			case "vfl-authority":
				key = "VFL-AUTHORITY_TAG"
			case "sidecar":
				key = "SIDECAR_TAG"
			}
			t.Setenv(key, "")
			if got := getMicroserviceTag(name); got != "fed-encrypt-dynamic" {
				t.Fatalf("default tag = %q", got)
			}
			t.Setenv("MICROSERVICE_TAG", "shared")
			if got := getMicroserviceTag(name); got != "shared" {
				t.Fatalf("shared tag = %q", got)
			}
			t.Setenv(key, "specific")
			if got := getMicroserviceTag(name); got != "specific" {
				t.Fatalf("service override tag = %q", got)
			}
		})
	}
}
