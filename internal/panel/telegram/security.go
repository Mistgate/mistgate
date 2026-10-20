package telegram

import (
	"context"
	"time"

	"github.com/mistgate/mistgate/internal/panel/auth"
)

// Security events (auth.SetEventHook): the owner hears of a lockout, of a sign-in from an address this panel has not seen
// lately, and of a change to anybody's way of signing in; the admin concerned hears of the last two about their own account.
// Failed sign-ins alone are not sent (the lockout is the news), nor is the setup of the first admin.

// Security is the auth event hook. It is called on its own goroutine and may block.
func (s *Service) Security(e auth.Event) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second) // D1 on the edge: a hung read must not hold the hook
	defer cancel()
	name := ""
	if e.AdminID != "" {
		if a, err := s.st.Admin(ctx, e.AdminID); err == nil {
			name = a.DisplayName
		}
	}
	switch e.Kind {
	case auth.EventLockout:
		s.enqueue(item{to: ownerOnly, text: func(l L, _ string) string { return lockoutText(l, e.Login, e.IP, e.Until) }})
	case auth.EventSignIn:
		if !s.newAddress(e.AdminID, e.IP) {
			return
		}
		s.enqueue(item{to: ownerAnd(e.AdminID), text: func(l L, _ string) string { return signInText(l, name, e.Method, e.IP) }})
	case auth.EventPasskeyAdded, auth.EventPasskeyRemoved, auth.EventPasswordChanged, auth.EventTOTPRebound, auth.EventPasswordAdded:
		s.enqueue(item{to: ownerAnd(e.AdminID), text: func(l L, _ string) string { return methodChangedText(l, name, e.Kind, e.IP) }})
	}
}

// newAddress remembers where an admin signed in from and reports whether it was a new place (not seen in the last 30 days
// by this process; a restart forgets, which only means one more message).
func (s *Service) newAddress(adminID, ip string) bool {
	if adminID == "" {
		return false
	}
	key, now := adminID+"|"+ip, s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	seen, ok := s.seenIP[key]
	s.seenIP[key] = now
	if len(s.seenIP) > 1000 { // a runaway list is a bug, not a feature
		for k, t := range s.seenIP {
			if now.Sub(t) > ipSeenFor {
				delete(s.seenIP, k)
			}
		}
	}
	return !ok || now.Sub(seen) > ipSeenFor
}

func ipLine(l L, ip string) string {
	if ip == "" {
		return ""
	}
	return l.pick("From address ", "С адреса ", "来源地址：") + data(ip, 45)
}

func lockoutText(l L, login, ip string, until time.Time) string {
	who := ""
	if login != "" {
		who = l.pick("Login: ", "Логин: ", "用户名：") + "<b>" + data(login, 60) + "</b>"
	}
	end := ""
	if !until.IsZero() {
		end = l.pick("Locked until ", "Заблокирован до ", "锁定至 ") + until.UTC().Format("15:04") + " UTC" + l.stop()
	}
	return join("<b>"+l.pick("Sign-in locked after too many failed attempts", "Вход заблокирован после череды неудачных попыток", "多次登录失败，登录已锁定")+"</b>", who, ipLine(l, ip), end)
}

func signInText(l L, name, method, ip string) string {
	how := ""
	switch method {
	case "passkey":
		how = l.pick(" with a passkey", " по passkey", "（通行密钥）")
	case "password":
		how = l.pick(" with a password", " по паролю", "（密码）")
	}
	return join("<b>"+l.pick("New sign-in to the panel", "Новый вход в панель", "面板有新的登录")+"</b>",
		"<b>"+data(name, 60)+"</b>"+l.pick(" signed in", " вошёл(а)", " 已登录")+how+l.stop(), ipLine(l, ip))
}

func methodChangedText(l L, name, kind, ip string) string {
	what := ""
	switch kind {
	case auth.EventPasskeyAdded:
		what = l.pick("a passkey was added", "добавлен passkey", "已添加通行密钥")
	case auth.EventPasskeyRemoved:
		what = l.pick("a passkey was removed", "удалён passkey", "已移除通行密钥")
	case auth.EventPasswordChanged:
		what = l.pick("the password was changed", "пароль изменён", "密码已更改")
	case auth.EventTOTPRebound:
		what = l.pick("the authenticator app was re-bound", "приложение-аутентификатор привязано заново", "身份验证器应用已重新绑定")
	case auth.EventPasswordAdded:
		what = l.pick("a password login was added", "добавлен вход по паролю", "已添加密码登录")
	}
	return join("<b>"+l.pick("A sign-in method changed", "Изменился способ входа", "登录方式已更改")+"</b>",
		"<b>"+data(name, 60)+"</b>"+l.pick(": ", ": ", "：")+what+l.stop(), ipLine(l, ip))
}
