package dnsdefaults

import (
	"reflect"
	"testing"
)

func TestForCountry(t *testing.T) {
	tests := []struct {
		country string
		want    []string
	}{
		{"RU", []string{"77.88.8.8", "77.88.8.1"}},
		{"ru", []string{"77.88.8.8", "77.88.8.1"}},
		{"DE", []string{"1.1.1.1", "8.8.8.8"}},
		{"", []string{"1.1.1.1", "8.8.8.8"}},
	}
	for _, tc := range tests {
		t.Run(tc.country, func(t *testing.T) {
			got := ForCountry(tc.country)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("ForCountry(%q) = %v, want %v", tc.country, got, tc.want)
			}
			got[0] = "changed"
			if again := ForCountry(tc.country); !reflect.DeepEqual(again, tc.want) {
				t.Fatalf("ForCountry(%q) returned shared storage: %v", tc.country, again)
			}
		})
	}
}
