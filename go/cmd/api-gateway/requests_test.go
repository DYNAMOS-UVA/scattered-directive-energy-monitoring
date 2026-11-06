package main

import (
	"reflect"
	"testing"
)

func TestOrderedClientAuthsIsStable(t *testing.T) {
	clients := map[string]string{
		"clienttwo":   "two",
		"clientone":   "one",
		"clientthree": "three",
	}
	want := []string{"clientone", "clientthree", "clienttwo"}

	for i := 0; i < 100; i++ {
		if got := orderedClientAuths(clients); !reflect.DeepEqual(got, want) {
			t.Fatalf("orderedClientAuths() = %v, want %v", got, want)
		}
	}
}

func TestOrderedClientAuthsBreaksCaseInsensitiveTies(t *testing.T) {
	clients := map[string]string{
		"clientone": "lower",
		"CLIENTONE": "upper",
	}
	want := []string{"CLIENTONE", "clientone"}

	if got := orderedClientAuths(clients); !reflect.DeepEqual(got, want) {
		t.Fatalf("orderedClientAuths() = %v, want %v", got, want)
	}
}

func TestValidateGradientCount(t *testing.T) {
	if err := validateGradientCount(3, 3); err != nil {
		t.Fatalf("validateGradientCount(3, 3) returned error: %v", err)
	}
	if err := validateGradientCount(2, 3); err == nil {
		t.Fatal("validateGradientCount(2, 3) did not reject a missing gradient")
	}
}
