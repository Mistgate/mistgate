package webproxy

import (
	"encoding/hex"
	"sync"
	"testing"
)

func TestCapabilityProtocolVectors(t *testing.T) {
	vectors := []struct {
		secret string
		want   string
	}{
		{
			secret: "000102030405060708090a0b0c0d0e0f",
			want:   "MHLEY5PmW1GWqJkSrlmJpvJUiLhBH_QKy6yKg8a0JPk",
		},
		{
			secret: "dd000102030405060708090a0b0c0d0e0f",
			want:   "IpJrt3e7sKtzPyoXy6w-Zj6GGEvsvclN66JzQEfPYLA",
		},
	}
	for _, vector := range vectors {
		secret, err := hex.DecodeString(vector.secret)
		if err != nil {
			t.Fatal(err)
		}
		got, err := DeriveCapability("proxy.example.com", secret)
		if err != nil {
			t.Fatal(err)
		}
		if got != vector.want {
			t.Fatal("capability vector mismatch")
		}
	}
}

func TestCapabilityHostNormalization(t *testing.T) {
	got, err := DeriveCapability("PROXY.Example.COM", []byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15})
	if err != nil {
		t.Fatal(err)
	}
	if got != "MHLEY5PmW1GWqJkSrlmJpvJUiLhBH_QKy6yKg8a0JPk" {
		t.Fatal("host case was not normalized")
	}
	for _, host := range []string{"", "proxy.example.com.", "proxy.example.com:443", "proxy.Ex\u00e4mple.com", "bad_label.example"} {
		if _, err := normalizeHost(host); err == nil {
			t.Fatalf("normalizeHost accepted %q", host)
		}
	}
}

func TestCapabilityIndexAcceptsBothSecretFormsAndKeepsPublishedSnapshots(t *testing.T) {
	plain := []byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15}
	dd := append([]byte{0xdd}, plain...)
	index, err := buildCapabilityIndex("proxy.example.com", []Credential{{ID: "de1", Secret: plain}})
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range [][]byte{plain, dd} {
		capability, err := DeriveCapability("proxy.example.com", secret)
		if err != nil {
			t.Fatal(err)
		}
		if id, ok := index.lookup(capability); !ok || id != "de1" {
			t.Fatalf("Lookup returned %q, %t", id, ok)
		}
	}

	otherSecret := []byte("0123456789abcdef")
	otherCapability, err := DeriveCapability("proxy.example.com", otherSecret)
	if err != nil {
		t.Fatal(err)
	}
	otherIndex, err := buildCapabilityIndex("proxy.example.com", []Credential{{ID: "nl1", Secret: otherSecret}})
	if err != nil {
		t.Fatal(err)
	}
	if id, ok := otherIndex.lookup(otherCapability); !ok || id != "nl1" {
		t.Fatalf("new snapshot lookup = %q, %t", id, ok)
	}
	oldCapability, _ := DeriveCapability("proxy.example.com", plain)
	if id, ok := index.lookup(oldCapability); !ok || id != "de1" {
		t.Fatal("publishing a new snapshot mutated the old one")
	}
}

func TestCapabilityIndexRejectsAmbiguousSnapshot(t *testing.T) {
	secret := []byte("0123456789abcdef")
	index, err := buildCapabilityIndex("proxy.example.com", []Credential{{ID: "de1", Secret: secret}})
	if err != nil {
		t.Fatal(err)
	}
	capability, err := DeriveCapability("proxy.example.com", secret)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := buildCapabilityIndex("proxy.example.com", []Credential{{ID: "de1", Secret: secret}, {ID: "nl1", Secret: secret}}); err == nil {
		t.Fatal("index accepted an ambiguous capability")
	}
	if id, ok := index.lookup(capability); !ok || id != "de1" {
		t.Fatalf("failed build changed snapshot: %q, %t", id, ok)
	}
}

func TestCapabilityIndexConcurrentReaders(t *testing.T) {
	secret := []byte("0123456789abcdef")
	index, err := buildCapabilityIndex("proxy.example.com", []Credential{{ID: "de1", Secret: secret}})
	if err != nil {
		t.Fatal(err)
	}
	capability, err := DeriveCapability("proxy.example.com", secret)
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	for reader := 0; reader < 4; reader++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := 0; n < 100; n++ {
				if id, ok := index.lookup(capability); !ok || id != "de1" {
					t.Errorf("concurrent Lookup = %q, %t", id, ok)
					return
				}
			}
		}()
	}
	wg.Wait()
}
