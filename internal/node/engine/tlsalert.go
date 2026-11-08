package engine

// CertificateAlert reports a TLS alert that refuses the peer's certificate (RFC 8446 section 6.2): bad_certificate (42),
// unsupported_certificate (43), certificate_revoked (44), certificate_expired (45), certificate_unknown (46) and unknown_ca
// (48). An app that sends one got the node's certificate and did not trust it (agent.proto LinkCounters.tls_cert_refused).
func CertificateAlert(alert uint8) bool {
	return alert >= 42 && alert <= 46 || alert == 48
}
