package provision

import (
	"context"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"
	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/auth"
)

// AdminPagePath is the exact path used by the authenticated panel wizard. The HTTP
// server also serves its slash-suffixed form without redirecting.
const AdminPagePath = "/nodes/install"

const (
	pageBodyLimit  = 32 << 10
	pageEventLimit = 100
)

type installPageData struct {
	Lang        pageLang
	Step        string
	Message     string
	HomeHref    string
	NodesHref   string
	Form        installForm
	Fingerprint string
	Preflight   *preflightView
	Job         *jobView
	Jobs        []jobView
	Access      []serverAccessView
	Events      []eventView
	Refresh     bool
}

type installForm struct {
	Host        string
	Port        string
	Username    string
	Name        string
	Address     string
	CountryCode string
	Location    string
	Provider    string
}

type serverAccessView struct {
	ID, Name, Host, Username, ConfiguredAt string
	Pending, Retired                       bool
}

type preflightView struct {
	Distribution   string
	Version        string
	Kernel         string
	Architecture   string
	CPUCount       uint32
	Memory         uint64 // bytes
	Disk           uint64 // bytes
	Systemd        bool
	PanelReachable bool
}

type jobView struct {
	ID         string
	Name       string
	Host       string
	State      string
	StateLabel string
	PhaseLabel string
	ErrorLabel string
	UpdatedAt  string
	CanCancel  bool
	CanRetry   bool
}

type eventView struct {
	Label     string
	CreatedAt string
}

// PageHandler serves the Go-rendered SSH installation wizard. The HTTP server wraps it
// in the same owner-only session and cross-origin protections as the admin API.
func (s *Service) PageHandler() http.Handler {
	return http.HandlerFunc(s.servePage)
}

func (s *Service) servePage(w http.ResponseWriter, r *http.Request) {
	if _, ok := auth.AdminFrom(r.Context()); !ok {
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if r.Method == http.MethodGet {
		s.getPage(w, r)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := parseInstallForm(w, r); err != nil {
		s.renderForRequest(w, r, http.StatusBadRequest, installPageData{Step: "host", Message: "Форма повреждена или слишком велика. Начните проверку заново."})
		return
	}
	s.postPage(w, r)
}

func parseInstallForm(w http.ResponseWriter, r *http.Request) error {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/x-www-form-urlencoded" {
		return errors.New("unsupported form encoding")
	}
	r.Body = http.MaxBytesReader(w, r.Body, pageBodyLimit)
	return r.ParseForm()
}

func (s *Service) getPage(w http.ResponseWriter, r *http.Request) {
	jobID := strings.TrimSpace(r.URL.Query().Get("job"))
	if jobID != "" {
		job, err := s.GetNodeProvision(r.Context(), connect.NewRequest(&adminv1.GetNodeProvisionRequest{JobId: jobID}))
		if err != nil {
			status := http.StatusInternalServerError
			message := "Не удалось загрузить задание. Попробуйте ещё раз."
			if connect.CodeOf(err) == connect.CodeNotFound {
				status, message = http.StatusNotFound, "Задание не найдено. Возможно, оно уже удалено."
			}
			s.renderForRequest(w, r, status, installPageData{Step: "host", Message: message})
			return
		}
		events, err := s.ListNodeProvisionEvents(r.Context(), connect.NewRequest(&adminv1.ListNodeProvisionEventsRequest{
			JobId: jobID, Limit: pageEventLimit,
		}))
		if err != nil {
			s.renderForRequest(w, r, http.StatusInternalServerError, installPageData{Step: "host", Message: "Не удалось загрузить события задания."})
			return
		}
		view := s.jobView(job.Msg.Job)
		page := installPageData{Step: "job", Job: &view, Refresh: view.State == "queued" || view.State == "running" || view.State == "cancel_requested"}
		if r.URL.Query().Get("cancelled") == "1" {
			if view.State == "cancelled" && job.Msg.Job.ErrorCode == "cancelled_before_start" {
				page.Message = "Задание отменено до подключения к серверу; временные SSH-данные удалены."
			} else {
				page.Message = "Установка остановлена. Проверьте сервер: часть SSH-команд могла уже выполниться."
			}
		}
		for _, event := range events.Msg.Events {
			page.Events = append(page.Events, makeEventView(event))
		}
		s.renderForRequest(w, r, http.StatusOK, page)
		return
	}
	jobs, err := s.ListNodeProvisions(r.Context(), connect.NewRequest(&adminv1.ListNodeProvisionsRequest{}))
	if err != nil {
		s.renderForRequest(w, r, http.StatusInternalServerError, installPageData{Step: "host", Message: "Не удалось загрузить задания установки."})
		return
	}
	page := installPageData{Step: "host"}
	if r.URL.Query().Get("password_rotated") == "1" {
		page.Message = "Пароль изменён, новый SSH-вход проверен."
	}
	for _, job := range jobs.Msg.Jobs {
		page.Jobs = append(page.Jobs, s.jobView(job))
	}
	access, err := s.ListNodeServerAccess(r.Context(), connect.NewRequest(&adminv1.ListNodeServerAccessRequest{}))
	if err != nil {
		s.renderForRequest(w, r, http.StatusInternalServerError, installPageData{Step: "host", Message: "Не удалось загрузить список SSH-доступов."})
		return
	}
	for _, item := range access.Msg.Access {
		page.Access = append(page.Access, serverAccessView{
			ID: item.NodeId, Name: item.NodeName, Host: net.JoinHostPort(item.Host, strconv.FormatUint(uint64(item.Port), 10)),
			Username: item.Username, ConfiguredAt: time.Unix(item.ConfiguredUnix, 0).Local().Format("2006-01-02 15:04"),
			Pending: item.RotationPending, Retired: item.NodeRetired,
		})
	}
	s.renderForRequest(w, r, http.StatusOK, page)
}

func (s *Service) postPage(w http.ResponseWriter, r *http.Request) {
	values := r.PostForm
	form := formValues(values)
	switch values.Get("action") {
	case "fingerprint":
		host, port, ok := formTarget(form)
		if !ok {
			s.renderForRequest(w, r, http.StatusBadRequest, installPageData{Step: "host", Message: "Укажите корректный адрес и порт SSH.", Form: form})
			return
		}
		result, err := s.GetSSHFingerprint(r.Context(), connect.NewRequest(&adminv1.GetSSHFingerprintRequest{Host: host, Port: port}))
		if err != nil {
			s.renderForRequest(w, r, http.StatusBadRequest, installPageData{
				Step: "host", Message: s.userMessage(err, "Не удалось получить SSH-отпечаток. Проверьте публичный адрес и доступность порта."), Form: form,
			})
			return
		}
		form.Host, form.Port = result.Msg.Host, strconv.FormatUint(uint64(result.Msg.Port), 10)
		s.renderForRequest(w, r, http.StatusOK, installPageData{Step: "fingerprint", Form: form, Fingerprint: result.Msg.Fingerprint})
	case "check":
		if !confirmed(values.Get("confirm_key")) {
			s.renderForRequest(w, r, http.StatusBadRequest, installPageData{Step: "host", Message: "Сначала получите и подтвердите отпечаток SSH host key."})
			return
		}
		host, port, ok := formTarget(form)
		fingerprint := values.Get("fingerprint")
		if !ok || !validFingerprint(fingerprint) {
			s.renderForRequest(w, r, http.StatusBadRequest, installPageData{Step: "host", Message: "Данные SSH-сервера недействительны. Начните проверку заново."})
			return
		}
		facts, err := s.checkSSH(r.Context(), host, port, fingerprint, form.Username, values.Get("password"))
		if err != nil {
			s.renderForRequest(w, r, http.StatusBadRequest, installPageData{
				Step: "fingerprint", Form: form, Fingerprint: fingerprint,
				Message: s.userMessage(err, "SSH-проверка не прошла. Проверьте логин и пароль, ключ сервера и требования к системе."),
			})
			return
		}
		s.renderForRequest(w, r, http.StatusOK, installPageData{Step: "preflight", Form: form, Fingerprint: fingerprint, Preflight: makePreflightView(facts)})
	case "start":
		s.startPageJob(w, r, values, form)
	case "retry":
		s.retryPageJob(w, r, values)
	case "cancel":
		s.cancelPageJob(w, r, values)
	case "rotate_password":
		s.rotatePagePassword(w, r, values)
	default:
		s.renderForRequest(w, r, http.StatusBadRequest, installPageData{Step: "host", Message: "Неизвестное действие. Начните установку заново."})
	}
}

func (s *Service) cancelPageJob(w http.ResponseWriter, r *http.Request, values url.Values) {
	jobID := strings.TrimSpace(values.Get("job_id"))
	if len(jobID) < 5 || len(jobID) > 64 || !confirmed(values.Get("confirm_cancel")) {
		s.renderForRequest(w, r, http.StatusBadRequest, installPageData{Step: "host", Message: "Выберите задание и подтвердите отмену."})
		return
	}
	if _, err := s.CancelNodeProvision(r.Context(), jobID); err != nil {
		status := http.StatusBadRequest
		if connect.CodeOf(err) == connect.CodeNotFound {
			status = http.StatusNotFound
		}
		s.renderForRequest(w, r, status, installPageData{Step: "host", Message: s.userMessage(err, "Не удалось отменить установку. Обновите страницу и проверьте состояние задания.")})
		return
	}
	query := url.Values{"job": []string{jobID}, "cancelled": []string{"1"}, "lang": []string{string(pageLanguage(r))}}
	w.Header().Set("Location", "?"+query.Encode())
	w.WriteHeader(http.StatusSeeOther)
}

func (s *Service) startPageJob(w http.ResponseWriter, r *http.Request, values url.Values, form installForm) {
	if !confirmed(values.Get("confirm_key")) || !confirmed(values.Get("confirm_install")) {
		s.renderForRequest(w, r, http.StatusBadRequest, installPageData{Step: "host", Message: "Перед запуском подтвердите ключ сервера и установку."})
		return
	}
	host, port, ok := formTarget(form)
	fingerprint := values.Get("fingerprint")
	if !ok || !validFingerprint(fingerprint) {
		s.renderForRequest(w, r, http.StatusBadRequest, installPageData{Step: "host", Message: "Данные SSH-сервера недействительны. Начните проверку заново."})
		return
	}
	password := values.Get("password")
	facts, err := s.checkSSH(r.Context(), host, port, fingerprint, form.Username, password)
	if err != nil {
		s.renderForRequest(w, r, http.StatusBadRequest, installPageData{
			Step: "fingerprint", Form: form, Fingerprint: fingerprint,
			Message: s.userMessage(err, "Повторная SSH-проверка не прошла. Проверьте пароль и попробуйте снова."),
		})
		return
	}
	result, err := s.StartNodeProvision(r.Context(), connect.NewRequest(&adminv1.StartNodeProvisionRequest{
		ConfirmInstall: true, Name: form.Name, Address: form.Address, CountryCode: form.CountryCode,
		Location: form.Location, Provider: form.Provider, SshHost: host, SshPort: port, Fingerprint: fingerprint, Password: password, SshUsername: form.Username,
	}))
	if err != nil {
		s.renderForRequest(w, r, http.StatusBadRequest, installPageData{
			Step: "preflight", Form: form, Fingerprint: fingerprint, Preflight: makePreflightView(facts),
			Message: s.userMessage(err, "Не удалось создать задание. Проверьте имя ноды и повторите действие."),
		})
		return
	}
	redirectToJob(w, result.Msg.Job.Id, pageLanguage(r))
}

func (s *Service) retryPageJob(w http.ResponseWriter, r *http.Request, values url.Values) {
	jobID := strings.TrimSpace(values.Get("job_id"))
	if len(jobID) > 64 || jobID == "" || !confirmed(values.Get("confirm_install")) {
		s.renderForRequest(w, r, http.StatusBadRequest, installPageData{Step: "host", Message: "Для повтора выберите задание и подтвердите запуск."})
		return
	}
	result, err := s.RetryNodeProvision(r.Context(), connect.NewRequest(&adminv1.RetryNodeProvisionRequest{
		JobId: jobID, ConfirmInstall: true, Password: values.Get("password"), SshUsername: sshUsername(values.Get("username")),
	}))
	if err != nil {
		s.getPageWithMessage(w, r, jobID, s.userMessage(err, "Не удалось повторить установку. Проверьте логин, пароль и состояние задания."))
		return
	}
	redirectToJob(w, result.Msg.Job.Id, pageLanguage(r))
}

func (s *Service) getPageWithMessage(w http.ResponseWriter, r *http.Request, jobID, message string) {
	job, err := s.GetNodeProvision(r.Context(), connect.NewRequest(&adminv1.GetNodeProvisionRequest{JobId: jobID}))
	if err != nil {
		s.renderForRequest(w, r, http.StatusBadRequest, installPageData{Step: "host", Message: message})
		return
	}
	events, err := s.ListNodeProvisionEvents(r.Context(), connect.NewRequest(&adminv1.ListNodeProvisionEventsRequest{
		JobId: jobID, Limit: pageEventLimit,
	}))
	if err != nil {
		s.renderForRequest(w, r, http.StatusInternalServerError, installPageData{Step: "host", Message: "Не удалось загрузить события задания."})
		return
	}
	view := s.jobView(job.Msg.Job)
	page := installPageData{Step: "job", Job: &view, Message: message, Refresh: view.State == "queued" || view.State == "running"}
	for _, event := range events.Msg.Events {
		page.Events = append(page.Events, makeEventView(event))
	}
	s.renderForRequest(w, r, http.StatusBadRequest, page)
}

func (s *Service) checkSSH(ctx context.Context, host string, port uint32, fingerprint, username, password string) (*adminv1.NodePreflight, error) {
	result, err := s.CheckSSH(ctx, connect.NewRequest(&adminv1.CheckSSHRequest{
		Host: host, Port: port, Fingerprint: fingerprint, Password: password, Username: sshUsername(username),
	}))
	if err != nil {
		return nil, err
	}
	return result.Msg.Preflight, nil
}

func (s *Service) rotatePagePassword(w http.ResponseWriter, r *http.Request, values url.Values) {
	nodeID := strings.TrimSpace(values.Get("node_id"))
	password := values.Get("new_password")
	if !confirmed(values.Get("confirm_rotation")) || nodeID == "" || len(nodeID) > 64 || len(password) < 12 || !validPassword(password) {
		s.renderForRequest(w, r, http.StatusBadRequest, installPageData{Step: "host", Message: "Подтвердите смену пароля и укажите новый пароль не короче 12 символов."})
		return
	}
	_, err := s.RotateNodeServerPassword(r.Context(), connect.NewRequest(&adminv1.RotateNodeServerPasswordRequest{
		NodeId: nodeID, NewPassword: password, Confirm: true,
	}))
	if err != nil {
		s.renderForRequest(w, r, http.StatusBadRequest, installPageData{
			Step: "host", Message: s.userMessage(err, "Не удалось подтвердить смену пароля. Доступ сохранён для восстановления."),
		})
		return
	}
	http.Redirect(w, r, "?password_rotated=1&lang="+string(pageLanguage(r)), http.StatusSeeOther)
}

func redirectToJob(w http.ResponseWriter, jobID string, l pageLang) {
	query := url.Values{"job": []string{jobID}, "lang": []string{string(l)}}
	w.Header().Set("Location", "?"+query.Encode())
	w.WriteHeader(http.StatusSeeOther)
}

func (s *Service) renderForRequest(w http.ResponseWriter, r *http.Request, status int, page installPageData) {
	page.HomeHref, page.NodesHref = pageLinks(r.URL.Path)
	page.Lang = pageLanguage(r)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Language", string(page.Lang))
	w.WriteHeader(status)
	if err := renderInstallPage(w, page.Lang, page); err != nil {
		if s.cfg.Log != nil {
			s.cfg.Log.Error("render node provisioning page", "err", err)
		}
	}
}

func pageLinks(path string) (home, nodes string) {
	up := "../"
	if strings.HasSuffix(path, "/") {
		up = "../../"
	}
	return up, up + "nodes"
}

func formValues(values url.Values) installForm {
	return installForm{
		Host: strings.TrimSpace(values.Get("host")), Port: strings.TrimSpace(values.Get("port")),
		Username: sshUsername(strings.TrimSpace(values.Get("username"))),
		Name:     strings.TrimSpace(values.Get("name")), Address: strings.TrimSpace(values.Get("address")),
		CountryCode: strings.TrimSpace(values.Get("country_code")), Location: strings.TrimSpace(values.Get("location")),
		Provider: strings.TrimSpace(values.Get("provider")),
	}
}

func formTarget(form installForm) (string, uint32, bool) {
	if form.Host == "" {
		return "", 0, false
	}
	port := uint64(22)
	if form.Port != "" {
		parsed, err := strconv.ParseUint(form.Port, 10, 16)
		if err != nil || parsed == 0 {
			return "", 0, false
		}
		port = parsed
	}
	return form.Host, uint32(port), true
}

func confirmed(value string) bool { return value == "yes" }

func makePreflightView(facts *adminv1.NodePreflight) *preflightView {
	if facts == nil {
		return nil
	}
	return &preflightView{
		Distribution: facts.Distribution, Version: facts.Version, Kernel: facts.Kernel, Architecture: facts.Architecture,
		CPUCount: facts.CpuCount, Memory: facts.MemoryBytes, Disk: facts.DiskAvailableBytes,
		Systemd: facts.Systemd, PanelReachable: facts.PanelReachable,
	}
}

func makeJobView(job *adminv1.NodeProvisionJob) jobView {
	if job == nil {
		return jobView{}
	}
	updated := time.Unix(job.UpdatedUnix, 0).Local().Format("2006-01-02 15:04:05")
	return jobView{
		ID: job.Id, Name: job.Name, Host: net.JoinHostPort(job.SshHost, strconv.FormatUint(uint64(job.SshPort), 10)),
		State: job.State, StateLabel: stateLabel(job.State), PhaseLabel: phaseLabel(job.Phase),
		ErrorLabel: errorLabel(job.ErrorCode), UpdatedAt: updated, CanCancel: job.State == "queued" || job.State == "running",
		CanRetry: job.State == "failed" || job.State == "cancelled",
	}
}

func makeEventView(event *adminv1.NodeProvisionEvent) eventView {
	if event == nil {
		return eventView{}
	}
	return eventView{
		Label:     eventLabel(event.Phase, event.Code),
		CreatedAt: time.Unix(event.CreatedUnix, 0).Local().Format("15:04:05"),
	}
}

func stateLabel(state string) string {
	switch state {
	case "queued":
		return "В очереди"
	case "running":
		return "Установка"
	case "cancel_requested":
		return "Остановка"
	case "cancelled":
		return "Отменено"
	case "completed":
		return "Нода подключена"
	case "failed":
		return "Ошибка"
	default:
		return "Состояние неизвестно"
	}
}

func phaseLabel(phase string) string {
	switch phase {
	case "queued":
		return "Ожидает запуска"
	case "preflight":
		return "Проверка сервера"
	case "firewall":
		return "Настройка firewall сервера"
	case "transfer":
		return "Передача агента"
	case "enrollment":
		return "Регистрация ноды"
	case "install":
		return "Установка службы"
	case "waiting_node":
		return "Ожидание подключения"
	case "completed":
		return "Завершено"
	case "cancelling":
		return "Остановка SSH-задания"
	case "cancelled":
		return "Отменено"
	case "failed":
		return "Завершилось с ошибкой"
	default:
		return "Подготовка"
	}
}

func eventLabel(phase, code string) string {
	switch code {
	case "queued":
		return "Задание поставлено в очередь"
	case "retry_requested":
		return "Запрошен повтор установки"
	case "cancel_requested":
		return "Владелец запросил отмену установки"
	case "cancelled_before_start":
		return "Задание отменено до подключения к серверу"
	case "remote_outcome_unknown":
		return "SSH-работа остановлена; состояние сервера нужно проверить"
	case "checking_host":
		return "Проверка сервера и SSH-ключа"
	case "preparing_host_firewall":
		return "Настройка активного firewall сервера"
	case "uploading_agent":
		return "Передача агента на сервер"
	case "enrolling_node":
		return "Регистрация ноды в панели"
	case "starting_agent":
		return "Настройка и запуск systemd-службы"
	case "waiting_for_agent":
		return "Ожидание подключения агента"
	case "agent_connected":
		return "Нода подключилась к панели"
	default:
		if code == "" {
			return phaseLabel(phase)
		}
		return phaseLabel(phase) + " · " + strings.ReplaceAll(code, "_", " ")
	}
}

// refusedTargetMessage is ssh_target_refused: the network the panel dials from refused the address or cannot reach it. Only
// the Cloudflare edition reports it; the words say "refused or unreachable" because the platform's own refusal looks
// the same as a closed port.
const refusedTargetMessage = "Сеть панели отказала в соединении с этим адресом, или сервер из неё недоступен. Панель подключается из сети Cloudflare: адреса Cloudflare, имена за прокси Cloudflare (оранжевое облако) и частные сети недоступны. Укажите собственный публичный адрес сервера или поставьте ноду командой вручную (в панели: добавление ноды, «Получить команду для ручной установки»)."

// edgeTimeoutMessage and edgeTimeoutLabel replace the generic timeout words on the Cloudflare edition, where the panel does
// not dial from the owner's computer or from a VPS the owner knows, but from Cloudflare's network.
const (
	edgeTimeoutMessage = "Панель не получила ответ от SSH-сервера вовремя. Панель подключается из сети Cloudflare, а не с вашего компьютера: SSH должен принимать входящий TCP из интернета (firewall хостера и сервера), а не только с вашего IP. Откройте его на время установки или поставьте ноду командой вручную."
	edgeTimeoutLabel   = "Сервер не ответил вовремя. Панель подключается из сети Cloudflare, а не с вашего компьютера: SSH должен принимать входящий TCP из интернета, а не только с вашего IP."
)

// onEdge reports the Cloudflare edition: only it enrols nodes over a link (Config.LinkURL).
func (s *Service) onEdge() bool { return s.cfg.LinkURL != "" }

// userMessage is userError with the edition's words for a timeout.
func (s *Service) userMessage(err error, fallback string) string {
	if s.onEdge() && connect.CodeOf(err) == connect.CodeDeadlineExceeded {
		return edgeTimeoutMessage
	}
	return userError(err, fallback)
}

// jobView is makeJobView with the edition's words for a timeout.
func (s *Service) jobView(job *adminv1.NodeProvisionJob) jobView {
	view := makeJobView(job)
	if s.onEdge() && job != nil && (job.ErrorCode == "ssh_connection_timeout" || job.ErrorCode == "ssh_preflight_timeout") {
		view.ErrorLabel = edgeTimeoutLabel
	}
	return view
}

func errorLabel(code string) string {
	switch code {
	case "remote_outcome_unknown":
		return "SSH-задание остановлено. Проверьте сервер: часть команд установки могла уже выполниться."
	case "ssh_target_invalid", "ssh_target_not_public":
		return "Укажите публичный адрес сервера, доступный по SSH."
	case "ssh_target_refused":
		return refusedTargetMessage
	case "ssh_host_key_changed":
		return "Ключ SSH изменился после подтверждения. Установка остановлена."
	case "ssh_authentication_failed":
		// root or a sudo user: the login is the owner's, so is the refusal
		return "Сервер отклонил SSH-логин или пароль. Проверьте их и повторите установку."
	case "ssh_password_auth_unavailable":
		return "Сервер не принимает вход по паролю, только по ключу. Поставьте ноду командой вручную или временно разрешите вход по паролю в sshd."
	case "ssh_connection_timeout", "ssh_preflight_timeout":
		return "Сервер не ответил вовремя. Проверьте сеть и порт SSH."
	case "unsupported_os", "unsupported_os_version":
		return "Поддерживаются Ubuntu 22.04+ и Debian 12+."
	case "unsupported_architecture":
		return "Поддерживаются архитектуры amd64 и arm64."
	case "systemd_required":
		return "На сервере не найден systemd."
	case "panel_unreachable":
		return "Сервер не может подключиться к панели. Проверьте адрес панели и исходящий доступ."
	case "insufficient_resources":
		return "На сервере недостаточно процессорных ресурсов или памяти."
	case "insufficient_disk_space":
		return "На сервере недостаточно свободного места для агента."
	case "node_not_connected":
		return "Агент установлен, но не подключился к панели вовремя."
	case "ssh_connection_failed", "ssh_connection_canceled":
		return "SSH-подключение прервано. Проверьте сервер и повторите установку."
	case "ssh_connection_refused", "ssh_connection_unavailable":
		return "Не удалось подключиться к SSH-серверу. Проверьте адрес и порт."
	case "ssh_preflight_failed", "ssh_preflight_canceled":
		return "Не удалось завершить предварительную проверку сервера."
	case "node_identity_mismatch", "node_identity_unreadable":
		return "На сервере обнаружена другая или повреждённая регистрация Mistgate."
	case "node_state_unavailable", "credentials_unavailable", "job_state_unavailable":
		return "Панели не удалось безопасно продолжить это задание. Проверьте журналы панели."
	case "node_retired":
		return "Эта нода была выведена из эксплуатации и не может быть зарегистрирована повторно."
	case "host_firewall_configuration_failed":
		return "Не удалось настроить активный firewall на сервере. Проверьте права SSH-пользователя и правила UFW/firewalld, затем повторите установку. Firewall хостера настраивается отдельно."
	case "agent_bundle_unavailable":
		return "В доверенном наборе релиза не найден агент для этой архитектуры."
	case "agent_transfer_failed":
		return "Не удалось передать агент на сервер."
	case "node_enrollment_failed", "node_enrollment_unavailable", "panel_ca_unavailable":
		return "Не удалось безопасно зарегистрировать ноду в панели."
	case "node_name_taken", "name_taken":
		return "Имя уже используется. Укажите другое имя ноды."
	case "systemd_install_failed", "agent_install_failed":
		return "Не удалось установить или запустить службу агента."
	case "":
		return ""
	default:
		return "Установка остановлена: " + strings.ReplaceAll(code, "_", " ")
	}
}

func userError(err error, fallback string) string {
	switch connect.CodeOf(err) {
	case connect.CodePermissionDenied:
		return "Требуется свежая проверка passkey. Войдите в панель и повторите действие."
	case connect.CodeUnauthenticated:
		return "Сессия панели завершилась. Войдите снова и повторите действие."
	case connect.CodeAlreadyExists:
		return "Имя ноды уже используется. Выберите другое имя."
	case connect.CodeDeadlineExceeded:
		return "Панель не получила ответ от SSH-сервера вовремя. Проверьте порт и firewall сервера или хостера, разрешив TCP-подключение с сервера панели."
	case connect.CodeFailedPrecondition:
		var connectErr *connect.Error
		if errors.As(err, &connectErr) && connectErr.Message() == "ssh_target_refused" {
			return refusedTargetMessage
		}
		return "Сервер не подходит для установки или настройка панели не завершена. Проверьте требования к системе."
	case connect.CodeInvalidArgument:
		return "Проверьте адрес, порт, логин, пароль и данные новой ноды."
	default:
		return fallback
	}
}

func (l pageLang) bytes(value uint64) string {
	units := [...]string{"Б", "КБ", "МБ", "ГБ", "ТБ", "ПБ"}
	if l != pageRU {
		units = [...]string{"B", "KB", "MB", "GB", "TB", "PB"}
	}
	const unit = uint64(1024)
	if value < unit {
		return fmt.Sprintf("%d %s", value, units[0])
	}
	div, exp := uint64(unit), 0
	for n := value / unit; n >= unit && exp < 4; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %s", float64(value)/float64(div), units[exp+1])
}

// ForwardHeader marks a request that the panel answers with 204 instead of serving: the edge Worker cannot hold an SSH
// socket, so it sends the original request to the Provisioner Worker. The value is "provision;v=<build>"; the Worker
// strips the header and tells the Provisioner which build asked, so that two Workers of different builds do not run
// one request between them.
const ForwardHeader = "X-Mistgate-Forward"

// ForwardMarker answers every request with that marker. It stands where the ProvisioningService and the install page
// would be served, inside the admin's sign-in check: a visitor who is not signed in never gets it.
func ForwardMarker(build string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set(ForwardHeader, "provision;v="+build)
		w.WriteHeader(http.StatusNoContent)
	})
}
