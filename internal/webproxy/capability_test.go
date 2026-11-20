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
	for _, host := range []string{"", "proxy.example.com.", "proxy.example.com:443", "proxy.Exämple.com", "bad_label.example"} {
		if _, err := NormalizeHost(host); err == nil {
			t.Fatalf("NormalizeHost accepted %q", host)
		}
	}
}

func TestCapabilityIndexAcceptsBothSecretFormsAndRebuilds(t *testing.T) {
	plain := []byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15}
	dd := append([]byte{0xdd}, plain...)
	index, err := NewCapabilityIndex("proxy.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if err := index.Rebuild([]Credential{{ID: "de1", Secret: plain}}); err != nil {
		t.Fatal(err)
	}
	for _, secret := range [][]byte{plain, dd} {
		capability, err := DeriveCapability("proxy.example.com", secret)
		if err != nil {
			t.Fatal(err)
		}
		if id, ok := index.Lookup(capability); !ok || id != "de1" {
			t.Fatalf("Lookup returned %q, %t", id, ok)
		}
	}

	otherSecret := []byte("0123456789abcdef")
	otherCapability, err := DeriveCapability("proxy.example.com", otherSecret)
	if err != nil {
		t.Fatal(err)
	}
	if err := index.Rebuild([]Credential{{ID: "nl1", Secret: otherSecret}}); err != nil {
		t.Fatal(err)
	}
	if id, ok := index.Lookup(otherCapability); !ok || id != "nl1" {
		t.Fatalf("rebuilt Lookup = %q, %t", id, ok)
	}
	oldCapability, _ := DeriveCapability("proxy.example.com", plain)
	if _, ok := index.Lookup(oldCapability); ok {
		t.Fatal("Rebuild retained a removed capability")
	}
}

func TestCapabilityIndexRejectedRebuildKeepsPreviousSnapshot(t *testing.T) {
	secret := []byte("0123456789abcdef")
	index, err := NewCapabilityIndex("proxy.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if err := index.Rebuild([]Credential{{ID: "de1", Secret: secret}}); err != nil {
		t.Fatal(err)
	}
	capability, err := DeriveCapability("proxy.example.com", secret)
	if err != nil {
		t.Fatal(err)
	}
	if err := index.Rebuild([]Credential{{ID: "de1", Secret: secret}, {ID: "nl1", Secret: secret}}); err == nil {
		t.Fatal("Rebuild accepted an ambiguous capability")
	}
	if id, ok := index.Lookup(capability); !ok || id != "de1" {
		t.Fatalf("failed rebuild changed snapshot: %q, %t", id, ok)
	}
}

func TestCapabilityIndexConcurrentReadersAndWriter(t *testing.T) {
	secret := []byte("0123456789abcdef")
	index, err := NewCapabilityIndex("proxy.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if err := index.Rebuild([]Credential{{ID: "de1", Secret: secret}}); err != nil {
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
				if id, ok := index.Lookup(capability); !ok || id != "de1" {
					t.Errorf("concurrent Lookup = %q, %t", id, ok)
					return
				}
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for n := 0; n < 100; n++ {
			if err := index.Rebuild([]Credential{{ID: "de1", Secret: secret}}); err != nil {
				t.Errorf("concurrent Rebuild: %v", err)
				return
			}
		}
	}()
	wg.Wait()
}
