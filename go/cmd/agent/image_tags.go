package main

import (
	"os"
	"strings"
)

func getMicroserviceTag(msName string) string {
	for _, key := range []string{
		strings.ReplaceAll(strings.ToUpper(msName), "-", "_") + "_TAG",
		strings.ToUpper(msName) + "_TAG",
		"MICROSERVICE_TAG",
	} {
		if tag := os.Getenv(key); tag != "" {
			return tag
		}
	}
	return "semi-overlap-fed-bcd"
}
