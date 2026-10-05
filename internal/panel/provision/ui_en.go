package provision

import (
	"net/http"
	"strings"
)

// pageLang is the language of the install page: its words are written in Russian in ui.go, and pageEnglish gives the
// English ones. A string missing here stays Russian on an English page (the test renders every step and catches it).
type pageLang string

const (
	pageRU pageLang = "ru"
	pageEN pageLang = "en"
)

// pageLanguage is the page's language: the admin's own, which the SPA passes as ?lang= (and every link of the page
// keeps), else the browser's first choice, Russian only when that is Russian (as the SPA decides).
func pageLanguage(r *http.Request) pageLang {
	switch r.URL.Query().Get("lang") {
	case "ru":
		return pageRU
	case "en":
		return pageEN
	}
	first, _, _ := strings.Cut(r.Header.Get("Accept-Language"), ",")
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(first)), "ru") {
		return pageRU
	}
	return pageEN
}

// tr gives a page string in l. A label followed by a code ("Установка остановлена: x", "Подготовка · y") keeps the code.
func (l pageLang) tr(s string) string {
	if l != pageEN || s == "" {
		return s
	}
	if e, ok := pageEnglish[s]; ok {
		return e
	}
	for _, sep := range []string{": ", " · "} {
		if head, tail, ok := strings.Cut(s, sep); ok {
			if e, ok := pageEnglish[head]; ok {
				return e + sep + tail
			}
		}
	}
	return s
}

var pageEnglish = map[string]string{
	// the page
	"Установка ноды — Mistgate":    "Install a node — Mistgate",
	"Назад в панель":               "Back to the panel",
	"Mistgate · Управление нодами": "Mistgate · Node management",
	"Установка ноды по SSH":        "Install a node over SSH",
	"Проверка сервера, подтверждение ключа SSH и установка агента Mistgate.": "Server check, SSH key confirmation and installation of the Mistgate agent.",
	"1 · Сервер":    "1 · Server",
	"2 · Ключ SSH":  "2 · SSH key",
	"3 · Проверка":  "3 · Check",
	"4 · Установка": "4 · Install",

	"Сверьте ключ SSH": "Compare the SSH key",
	"Mistgate не отправит пароль, пока вы не подтвердите, что этот ключ принадлежит вашему серверу.": "Mistgate sends no password until you confirm that this key belongs to your server.",
	"SHA-256 отпечаток SSH host key": "SHA-256 fingerprint of the SSH host key",
	"Я сверил отпечаток с ключом моего сервера и подтверждаю его.": "I compared the fingerprint with my server's key and confirm it.",
	"Данные новой ноды": "The new node",
	"Имя ноды":          "Node name",
	"Адрес ноды":        "Node address",
	"Код страны":        "Country code",
	"Регион или город":  "Region or city",
	"Провайдер":         "Provider",
	"Название хостинга": "Hosting company",
	"SSH-логин":         "SSH login",
	"Пароль SSH":        "SSH password",
	"Проверить сервер":  "Check the server",
	"Начать заново":     "Start over",
	"Подойдут root или отдельный SSH-пользователь с настроенным":                     "Use root or a separate SSH user with a working",
	"Пароль передаётся только в теле запроса и шифруется перед сохранением задания.": "The password travels only in the request body and is encrypted before the job is saved.",

	"Требования выполнены": "The requirements are met",
	"SSH-аутентификация и предварительные проверки прошли. Для запуска установки введите пароль ещё раз.": "The SSH sign-in and the checks passed. Enter the password once more to start the installation.",
	"Система":         "System",
	"Архитектура":     "Architecture",
	"Ядро":            "Kernel",
	"Процессоры":      "CPUs",
	"Память":          "Memory",
	"Свободное место": "Free disk space",
	"Доступен":        "Available",
	"Не найден":       "Not found",
	"Связь с панелью": "Reaches the panel",
	"Есть":            "Yes",
	"Нет":             "No",
	"Подтверждение установки": "Confirm the installation",
	"Пароль SSH пользователя": "SSH password of the user",
	"Подтверждаю отпечаток":   "I confirm the fingerprint",
	"Установить агент Mistgate и создать новую ноду. Существующая система не будет переустановлена.": "Install the Mistgate agent and create the new node. The existing system is not reinstalled.",
	"Запустить установку": "Start the installation",
	"Отмена":              "Cancel",

	"Задание установки": "Installation job",
	"SSH-сервер":        "SSH server",
	"Этап":              "Stage",
	"Обновлено":         "Updated",
	"Отменить установку. Если SSH-команды уже выполняются, сервер может быть изменён частично.": "Cancel the installation. If SSH commands are already running, the server may be partly changed.",
	"Отменить установку": "Cancel the installation",
	"К списку заданий":   "Back to the jobs",
	"Остановка запрошена. Панель завершит задание и удалит временные SSH-данные.": "Stop requested. The panel ends the job and deletes the temporary SSH data.",
	"События появятся после запуска задания.":                                     "Events appear once the job starts.",
	"Повторить установку": "Retry the installation",
	"Проверьте причину ошибки. Для повтора введите SSH-логин и его пароль: root или пользователь с passwordless sudo.": "Check the cause of the error. To retry, enter the SSH login and its password: root or a user with passwordless sudo.",
	"Повторно выполнить установку для этой ноды.":                                                                      "Run the installation for this node again.",
	"Повторить":    "Retry",
	"Открыть ноды": "Open nodes",
	"Статус обновляется автоматически каждые 4 секунды.": "The status refreshes every 4 seconds.",
	"Открыть список заданий":                             "Open the job list",
	"Вернуться к заданиям":                               "Back to the jobs",

	"Новая нода": "New node",
	"Мастер установит Mistgate Agent на Ubuntu 22.04+ или Debian 12+ с systemd. Можно войти как root или как SSH-пользователь с passwordless sudo.": "The wizard installs the Mistgate agent on Ubuntu 22.04+ or Debian 12+ with systemd. Log in as root or as an SSH user with passwordless sudo.",
	"Адрес SSH-сервера":                 "SSH server address",
	"node.example.com или публичный IP": "node.example.com or a public IP",
	"Порт SSH":        "SSH port",
	"Начать проверку": "Start the check",
	"Пароль не запрашивается до подтверждения отпечатка SSH host key.": "No password is asked for before you confirm the SSH host key fingerprint.",
	"Последние задания": "Recent jobs",
	"Нода":              "Node",
	"Состояние":         "State",
	"Открыть":           "Open",
	"Доступ к серверам": "Server access",
	"Пароли хранятся в зашифрованном виде. При смене панель сначала проверит новый вход и только затем заменит сохранённый пароль.": "Passwords are stored encrypted. On a change the panel checks the new login first and only then replaces the saved password.",
	"Подключение":    "Login",
	"Сменить пароль": "Change password",
	"Смена ожидает проверки; повторите её для восстановления.":                                                         "A change waits for its check; repeat it to recover.",
	"Нода выведена из флота: панель больше не меняет этот сервер. Пароль можно показать или забыть в настройках ноды.": "The node is retired: the panel no longer changes this server. Show or forget the password in the node's settings.",
	"Новый пароль SSH":            "New SSH password",
	"Сменить пароль пользователя": "Change the password of the user",
	"Сменить":                     "Change",
	"Изменения на сервере начинаются только после успешной проверки требований, подтверждения ключа и нажатия «Запустить установку».": "Nothing on the server changes until the requirements check passes, the key is confirmed and you press “Start the installation”.",

	// messages
	"Форма повреждена или слишком велика. Начните проверку заново.":                                                                              "The form is damaged or too large. Start the check again.",
	"Не удалось загрузить задание. Попробуйте ещё раз.":                                                                                          "Could not load the job. Try again.",
	"Задание не найдено. Возможно, оно уже удалено.":                                                                                             "Job not found. It may have been deleted.",
	"Не удалось загрузить события задания.":                                                                                                      "Could not load the job's events.",
	"Задание отменено до подключения к серверу; временные SSH-данные удалены.":                                                                   "The job was cancelled before it connected to the server; the temporary SSH data is deleted.",
	"Установка остановлена. Проверьте сервер: часть SSH-команд могла уже выполниться.":                                                           "The installation was stopped. Check the server: some SSH commands may already have run.",
	"Не удалось загрузить задания установки.":                                                                                                    "Could not load the installation jobs.",
	"Пароль изменён, новый SSH-вход проверен.":                                                                                                   "Password changed, the new SSH login is verified.",
	"Не удалось загрузить список SSH-доступов.":                                                                                                  "Could not load the SSH access list.",
	"Укажите корректный адрес и порт SSH.":                                                                                                       "Enter a valid SSH address and port.",
	"Не удалось получить SSH-отпечаток. Проверьте публичный адрес и доступность порта.":                                                          "Could not read the SSH fingerprint. Check the public address and that the port is reachable.",
	"Сначала получите и подтвердите отпечаток SSH host key.":                                                                                     "First read and confirm the SSH host key fingerprint.",
	"Данные SSH-сервера недействительны. Начните проверку заново.":                                                                               "The SSH server data is not valid. Start the check again.",
	"SSH-проверка не прошла. Проверьте логин и пароль, ключ сервера и требования к системе.":                                                     "The SSH check failed. Check the login and password, the server key and the system requirements.",
	"Неизвестное действие. Начните установку заново.":                                                                                            "Unknown action. Start the installation again.",
	"Выберите задание и подтвердите отмену.":                                                                                                     "Choose a job and confirm the cancellation.",
	"Не удалось отменить установку. Обновите страницу и проверьте состояние задания.":                                                            "Could not cancel the installation. Reload the page and check the job's state.",
	"Перед запуском подтвердите ключ сервера и установку.":                                                                                       "Confirm the server key and the installation before starting.",
	"Повторная SSH-проверка не прошла. Проверьте пароль и попробуйте снова.":                                                                     "The repeated SSH check failed. Check the password and try again.",
	"Не удалось создать задание. Проверьте имя ноды и повторите действие.":                                                                       "Could not create the job. Check the node name and try again.",
	"Для повтора выберите задание и подтвердите запуск.":                                                                                         "To retry, choose a job and confirm the start.",
	"Не удалось повторить установку. Проверьте логин, пароль и состояние задания.":                                                               "Could not retry the installation. Check the login, the password and the job's state.",
	"Подтвердите смену пароля и укажите новый пароль не короче 12 символов.":                                                                     "Confirm the change and enter a new password of at least 12 characters.",
	"Не удалось подтвердить смену пароля. Доступ сохранён для восстановления.":                                                                   "Could not verify the password change. The access is kept for recovery.",
	"Требуется свежая проверка passkey. Войдите в панель и повторите действие.":                                                                  "A fresh passkey check is needed. Sign in to the panel and repeat the action.",
	"Сессия панели завершилась. Войдите снова и повторите действие.":                                                                             "The panel session ended. Sign in again and repeat the action.",
	"Имя ноды уже используется. Выберите другое имя.":                                                                                            "The node name is taken. Choose another one.",
	"Панель не получила ответ от SSH-сервера вовремя. Проверьте порт и firewall сервера или хостера, разрешив TCP-подключение с сервера панели.": "The panel got no answer from the SSH server in time. Check the port and the server's or the hoster's firewall: allow TCP from the panel's server.",
	"Сервер не подходит для установки или настройка панели не завершена. Проверьте требования к системе.":                                        "The server does not meet the requirements, or the panel's setup is not finished. Check the system requirements.",
	"Проверьте адрес, порт, логин, пароль и данные новой ноды.":                                                                                  "Check the address, port, login, password and the new node's data.",

	// job states, phases and events
	"В очереди":                          "Queued",
	"Установка":                          "Installing",
	"Остановка":                          "Stopping",
	"Отменено":                           "Cancelled",
	"Нода подключена":                    "Node connected",
	"Ошибка":                             "Error",
	"Состояние неизвестно":               "Unknown state",
	"Ожидает запуска":                    "Waiting to start",
	"Проверка сервера":                   "Checking the server",
	"Настройка firewall сервера":         "Setting up the server firewall",
	"Передача агента":                    "Transferring the agent",
	"Регистрация ноды":                   "Registering the node",
	"Установка службы":                   "Installing the service",
	"Ожидание подключения":               "Waiting for the connection",
	"Завершено":                          "Done",
	"Остановка SSH-задания":              "Stopping the SSH job",
	"Завершилось с ошибкой":              "Ended with an error",
	"Подготовка":                         "Preparing",
	"Задание поставлено в очередь":       "The job is queued",
	"Запрошен повтор установки":          "A retry was requested",
	"Владелец запросил отмену установки": "The owner asked to cancel the installation",
	"Задание отменено до подключения к серверу":                 "The job was cancelled before it connected to the server",
	"SSH-работа остановлена; состояние сервера нужно проверить": "The SSH work was stopped; check the server's state",
	"Проверка сервера и SSH-ключа":                              "Checking the server and the SSH key",
	"Настройка активного firewall сервера":                      "Setting up the server's active firewall",
	"Передача агента на сервер":                                 "Transferring the agent to the server",
	"Регистрация ноды в панели":                                 "Registering the node in the panel",
	"Настройка и запуск systemd-службы":                         "Setting up and starting the systemd service",
	"Ожидание подключения агента":                               "Waiting for the agent to connect",
	"Нода подключилась к панели":                                "The node connected to the panel",

	// job errors
	"SSH-задание остановлено. Проверьте сервер: часть команд установки могла уже выполниться.": "The SSH job was stopped. Check the server: some installation commands may already have run.",
	"Укажите публичный адрес сервера, доступный по SSH.":                                       "Enter the server's public address that SSH can reach.",
	"Ключ SSH изменился после подтверждения. Установка остановлена.":                           "The SSH key changed after you confirmed it. The installation is stopped.",
	"Сервер отклонил SSH-логин или пароль. Проверьте их и повторите установку.":                "The server refused the SSH login or password. Check them and retry the installation.",
	"Сервер не ответил вовремя. Проверьте сеть и порт SSH.":                                    "The server did not answer in time. Check the network and the SSH port.",
	"Поддерживаются Ubuntu 22.04+ и Debian 12+.":                                               "Ubuntu 22.04+ and Debian 12+ are supported.",
	"Поддерживаются архитектуры amd64 и arm64.":                                                "The amd64 and arm64 architectures are supported.",
	"На сервере не найден systemd.":                                                            "The server has no systemd.",
	"Сервер не может подключиться к панели. Проверьте адрес панели и исходящий доступ.":        "The server cannot reach the panel. Check the panel's address and the server's outbound access.",
	"На сервере недостаточно процессорных ресурсов или памяти.":                                "The server has too little CPU or memory.",
	"На сервере недостаточно свободного места для агента.":                                     "The server has too little free disk space for the agent.",
	"Агент установлен, но не подключился к панели вовремя.":                                    "The agent is installed but did not connect to the panel in time.",
	"SSH-подключение прервано. Проверьте сервер и повторите установку.":                        "The SSH connection broke. Check the server and retry the installation.",
	"Не удалось подключиться к SSH-серверу. Проверьте адрес и порт.":                           "Could not connect to the SSH server. Check the address and the port.",
	"Не удалось завершить предварительную проверку сервера.":                                   "The server check could not finish.",
	"На сервере обнаружена другая или повреждённая регистрация Mistgate.":                      "The server has another or a damaged Mistgate registration.",
	"Панели не удалось безопасно продолжить это задание. Проверьте журналы панели.":            "The panel could not safely continue this job. Check the panel's logs.",
	"Эта нода была выведена из эксплуатации и не может быть зарегистрирована повторно.":        "This node was retired and cannot be registered again.",
	"Не удалось настроить активный firewall на сервере. Проверьте права SSH-пользователя и правила UFW/firewalld, затем повторите установку. Firewall хостера настраивается отдельно.": "Could not set up the server's active firewall. Check the SSH user's rights and the UFW/firewalld rules, then retry. The hoster's firewall is set up separately.",
	"В доверенном наборе релиза не найден агент для этой архитектуры.":                                                                                                                 "The trusted release bundle has no agent for this architecture.",
	"Не удалось передать агент на сервер.":                 "Could not transfer the agent to the server.",
	"Не удалось безопасно зарегистрировать ноду в панели.": "Could not safely register the node in the panel.",
	"Имя уже используется. Укажите другое имя ноды.":       "The name is taken. Choose another node name.",
	"Не удалось установить или запустить службу агента.":   "Could not install or start the agent service.",
	"Установка остановлена":                                "The installation stopped",
}
