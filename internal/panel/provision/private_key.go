package provision

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rsa"
	"encoding/pem"
	"errors"

	"connectrpc.com/connect"
	"golang.org/x/crypto/ssh"
)

const maxPrivateKeyBytes = 64 << 10

var (
	errInvalidSSHLogin            = errors.New("invalid SSH credentials or target")
	errPrivateKeyInvalid          = errors.New("ssh_private_key_invalid")
	errPrivateKeyPassphraseNeeded = errors.New("ssh_key_passphrase_required")
	errPrivateKeyPassphraseWrong  = errors.New("ssh_key_passphrase_invalid")
)

func parseSSHLogin(usePanelKey bool, password, privateKey, passphrase string) (string, []byte, error) {
	if usePanelKey {
		if password != "" || privateKey != "" || passphrase != "" {
			return "", nil, errInvalidSSHLogin
		}
		return "panel_key", nil, nil
	}
	if privateKey != "" {
		if password != "" {
			return "", nil, errInvalidSSHLogin
		}
		key, err := normalizePrivateKey(privateKey, passphrase)
		if err != nil {
			return "", nil, err
		}
		return "private_key", key, nil
	}
	if passphrase != "" || !validPassword(password) {
		return "", nil, errInvalidSSHLogin
	}
	return "password", nil, nil
}

// normalizePrivateKey accepts an unencrypted or passphrase-protected OpenSSH/PEM key and returns an unencrypted
// OpenSSH encoding for the sealed job secret. The caller must clear the returned bytes as soon as it seals or uses them.
func normalizePrivateKey(raw, passphrase string) ([]byte, error) {
	if raw == "" || len(raw) > maxPrivateKeyBytes || len(passphrase) > 1024 {
		return nil, errPrivateKeyInvalid
	}
	input := []byte(raw)
	defer clear(input)
	pass := []byte(passphrase)
	defer clear(pass)
	key, err := ssh.ParseRawPrivateKey(input)
	if err != nil {
		var missing *ssh.PassphraseMissingError
		if !errors.As(err, &missing) {
			return nil, errPrivateKeyInvalid
		}
		if len(pass) == 0 {
			return nil, errPrivateKeyPassphraseNeeded
		}
		key, err = ssh.ParseRawPrivateKeyWithPassphrase(input, pass)
		if err != nil {
			return nil, errPrivateKeyPassphraseWrong
		}
	}
	defer clearPrivateKey(key)
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil || signer == nil || signer.PublicKey() == nil {
		return nil, errPrivateKeyInvalid
	}
	bits, accepted := serverKeyBits(signer.PublicKey())
	if !accepted || signer.PublicKey().Type() == ssh.KeyAlgoRSA && bits < 2048 {
		return nil, errPrivateKeyInvalid
	}
	block, err := ssh.MarshalPrivateKey(key, "mistgate temporary SSH login")
	if err != nil {
		return nil, errPrivateKeyInvalid
	}
	return pem.EncodeToMemory(block), nil
}

func signerFromPrivateKey(encoded []byte) (ssh.Signer, any, error) {
	key, err := ssh.ParseRawPrivateKey(encoded)
	if err != nil {
		return nil, nil, errPrivateKeyInvalid
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil || signer == nil || signer.PublicKey() == nil {
		clearPrivateKey(key)
		return nil, nil, errPrivateKeyInvalid
	}
	return signer, key, nil
}

func clearPrivateKey(key any) {
	switch value := key.(type) {
	case ed25519.PrivateKey:
		clear(value)
	case *ed25519.PrivateKey: // what ParseRawPrivateKey returns for an OpenSSH ed25519 key
		if value != nil {
			clear(*value)
		}
	case *rsa.PrivateKey:
		if value != nil {
			if value.D != nil {
				value.D.SetInt64(0)
			}
			for _, prime := range value.Primes {
				if prime != nil {
					prime.SetInt64(0)
				}
			}
			for i := range value.Precomputed.CRTValues {
				crt := &value.Precomputed.CRTValues[i]
				if crt.Exp != nil {
					crt.Exp.SetInt64(0)
				}
				if crt.Coeff != nil {
					crt.Coeff.SetInt64(0)
				}
				if crt.R != nil {
					crt.R.SetInt64(0)
				}
			}
			if value.Precomputed.Dp != nil {
				value.Precomputed.Dp.SetInt64(0)
			}
			if value.Precomputed.Dq != nil {
				value.Precomputed.Dq.SetInt64(0)
			}
			if value.Precomputed.Qinv != nil {
				value.Precomputed.Qinv.SetInt64(0)
			}
		}
	case *ecdsa.PrivateKey:
		if value != nil && value.D != nil {
			value.D.SetInt64(0)
		}
	}
}

func privateKeyConnectError(err error) error {
	switch {
	case errors.Is(err, errPrivateKeyPassphraseNeeded):
		return connect.NewError(connect.CodeFailedPrecondition, errPrivateKeyPassphraseNeeded)
	case errors.Is(err, errPrivateKeyPassphraseWrong):
		// not Unauthenticated: that code means the admin's own session to the web client
		return connect.NewError(connect.CodeInvalidArgument, errPrivateKeyPassphraseWrong)
	default:
		return connect.NewError(connect.CodeInvalidArgument, errPrivateKeyInvalid)
	}
}
