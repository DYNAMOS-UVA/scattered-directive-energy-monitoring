package main

import (
	"os"
	"strings"
)

func getMicroserviceTag(msName string) string {
	if tag := os.Getenv(strings.ToUpper(msName) + "_TAG"); tag != "" {
		return tag
	}
	if tag := os.Getenv("MICROSERVICE_TAG"); tag != "" {
		return tag
	}
	return "fed-encrypt-dynamic"
}
