package auth

import "time"

// Event kinds passed to the hook installed with SetEventHook. The panel installs one (internal/panel/telegram) that tells the
// owner of a lockout, a sign-in from a new address and a change to a sign-in method; the audit log stays the record (a
// lockout is its "lockout" row).
const (
	EventSetup          = "setup"           // the first admin was created
	EventSignIn         = "sign_in"         // a session was opened (Method says how)
	EventSignInFailed   = "sign_in_failed"  // a wrong password or code
	EventLockout        = "lockout"         // a login was locked after too many failures
	EventPasskeyAdded   = "passkey_added"   // Settings -> Security
	EventPasskeyRemoved = "passkey_removed" // Settings -> Security
	// Settings -> Security -> "Password and code": a new password, a re-bound authenticator app, a new password login.
	EventPasswordChanged = "password_changed"
	EventTOTPRebound     = "totp_rebound"
	EventPasswordAdded   = "password_added"
)

// Event is what a hook gets to react to. It carries no secrets.
type Event struct {
	Kind    string
	Time    time.Time
	AdminID string    // "" when the login matched no admin
	Login   string    // password sign-ins: the login, only when it names a real admin
	Method  string    // "passkey" or "password" where it applies
	IP      string    // client address, "" if unknown
	Until   time.Time // EventLockout: when the lock ends
}

// SetEventHook installs fn, called once per event on its own goroutine, so it may block
// without slowing a sign-in down (and must be safe for concurrent use). nil removes it.
// Events are fire-and-forget: the audit log is the record.
func (s *Service) SetEventHook(fn func(Event)) {
	if fn == nil {
		s.hook.Store(nil)
		return
	}
	s.hook.Store(&fn)
}

func (s *Service) emit(e Event) {
	if h := s.hook.Load(); h != nil {
		e.Time = s.now()
		go (*h)(e)
	}
}
