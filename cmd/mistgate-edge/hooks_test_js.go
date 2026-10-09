//go:build js && wasm && edgeentrytest

package main

import (
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/json"
	"net/http"

	"github.com/mistgate/mistgate/internal/panel/fleet"
)

func withEdgeTestHooks(handler http.Handler, fl *fleet.Fleet) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// POST {nodeId, spki (base64 DER P-256 public key)}: a CA-signed node certificate row and the CA certificate.
		if r.URL.Path == "/__edge_bridge_test__/issue-node-cert" {
			var in struct {
				NodeID string `json:"nodeId"`
				SPKI   []byte `json:"spki"`
			}
			err := json.NewDecoder(r.Body).Decode(&in)
			var pub any
			if err == nil {
				pub, err = x509.ParsePKIXPublicKey(in.SPKI)
			}
			key, isECDSA := pub.(*ecdsa.PublicKey)
			if err != nil || !isECDSA {
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			row, err := fl.IssueNodeCert(in.NodeID, key)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"serial": row.Serial, "caId": row.CAID, "pem": row.PEM, "caPem": fl.CACertPEM(),
				"notBefore": row.NotBefore.Unix(), "notAfter": row.NotAfter.Unix(), "issuedAt": row.IssuedAt.Unix(),
			})
			return
		}
		if r.URL.Path == "/__edge_bridge_test__/cookies" {
			w.Header().Add("Set-Cookie", "first=one; Path=/")
			w.Header().Add("Set-Cookie", "second=two; Path=/")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte("cookie-test"))
			return
		}
		if r.URL.Path == "/__edge_bridge_test__/headers" {
			for _, value := range r.Header.Values("X-Multi") {
				w.Header().Add("X-Multi-Response", value)
			}
			_, _ = w.Write([]byte("header-test"))
			return
		}
		if r.URL.Path == "/__edge_bridge_test__/stream" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("stream-test"))
			w.(http.Flusher).Flush()
			return
		}
		handler.ServeHTTP(w, r)
	})
}
