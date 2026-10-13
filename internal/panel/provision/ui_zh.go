package provision

// zh: translation pass — values in this table are English placeholders keyed by the installer Russian source text.
var pageChinese = map[string]string{
	// the page
	"Установка ноды — Mistgate":    "Install a node — Mistgate",  // zh: translation pass
	"Назад в панель":               "Back to the panel",          // zh: translation pass
	"Mistgate · Управление нодами": "Mistgate · Node management", // zh: translation pass
	"Установка ноды по SSH":        "Install a node over SSH",    // zh: translation pass
	"Проверка сервера, подтверждение ключа SSH и установка агента Mistgate.": "Server check, SSH key confirmation and installation of the Mistgate agent.", // zh: translation pass
	"1 · Сервер":    "1 · Server",  // zh: translation pass
	"2 · Ключ SSH":  "2 · SSH key", // zh: translation pass
	"3 · Проверка":  "3 · Check",   // zh: translation pass
	"4 · Установка": "4 · Install", // zh: translation pass

	"Сверьте ключ SSH": "Compare the SSH key", // zh: translation pass
	"Mistgate не отправит пароль, пока вы не подтвердите, что этот ключ принадлежит вашему серверу.": "Mistgate sends no password until you confirm that this key belongs to your server.", // zh: translation pass
	"SHA-256 отпечаток SSH host key": "SHA-256 fingerprint of the SSH host key", // zh: translation pass
	"Я сверил отпечаток с ключом моего сервера и подтверждаю его.": "I compared the fingerprint with my server's key and confirm it.", // zh: translation pass
	"Данные новой ноды": "The new node",     // zh: translation pass
	"Имя ноды":          "Node name",        // zh: translation pass
	"Адрес ноды":        "Node address",     // zh: translation pass
	"Код страны":        "Country code",     // zh: translation pass
	"Регион или город":  "Region or city",   // zh: translation pass
	"Провайдер":         "Provider",         // zh: translation pass
	"Название хостинга": "Hosting company",  // zh: translation pass
	"SSH-логин":         "SSH login",        // zh: translation pass
	"Пароль SSH":        "SSH password",     // zh: translation pass
	"Проверить сервер":  "Check the server", // zh: translation pass
	"Начать заново":     "Start over",       // zh: translation pass
	"Подойдут root или отдельный SSH-пользователь с настроенным":                     "Use root or a separate SSH user with a working",                                          // zh: translation pass
	"Пароль передаётся только в теле запроса и шифруется перед сохранением задания.": "The password travels only in the request body and is encrypted before the job is saved.", // zh: translation pass

	"Требования выполнены": "The requirements are met", // zh: translation pass
	"SSH-аутентификация и предварительные проверки прошли. Для запуска установки введите пароль ещё раз.": "The SSH sign-in and the checks passed. Enter the password once more to start the installation.", // zh: translation pass
	"Система":         "System",            // zh: translation pass
	"Архитектура":     "Architecture",      // zh: translation pass
	"Ядро":            "Kernel",            // zh: translation pass
	"Процессоры":      "CPUs",              // zh: translation pass
	"Память":          "Memory",            // zh: translation pass
	"Свободное место": "Free disk space",   // zh: translation pass
	"Доступен":        "Available",         // zh: translation pass
	"Не найден":       "Not found",         // zh: translation pass
	"Связь с панелью": "Reaches the panel", // zh: translation pass
	"Есть":            "Yes",               // zh: translation pass
	"Нет":             "No",                // zh: translation pass
	"Подтверждение установки": "Confirm the installation",  // zh: translation pass
	"Пароль SSH пользователя": "SSH password of the user",  // zh: translation pass
	"Подтверждаю отпечаток":   "I confirm the fingerprint", // zh: translation pass
	"Установить агент Mistgate и создать новую ноду. Существующая система не будет переустановлена.": "Install the Mistgate agent and create the new node. The existing system is not reinstalled.", // zh: translation pass
	"Запустить установку": "Start the installation", // zh: translation pass
	"Отмена":              "Cancel",                 // zh: translation pass

	"Задание установки": "Installation job", // zh: translation pass
	"SSH-сервер":        "SSH server",       // zh: translation pass
	"Этап":              "Stage",            // zh: translation pass
	"Обновлено":         "Updated",          // zh: translation pass
	"Отменить установку. Если SSH-команды уже выполняются, сервер может быть изменён частично.": "Cancel the installation. If SSH commands are already running, the server may be partly changed.", // zh: translation pass
	"Отменить установку": "Cancel the installation", // zh: translation pass
	"К списку заданий":   "Back to the jobs",        // zh: translation pass
	"Остановка запрошена. Панель завершит задание и удалит временные SSH-данные.": "Stop requested. The panel ends the job and deletes the temporary SSH data.", // zh: translation pass
	"События появятся после запуска задания.":                                     "Events appear once the job starts.",                                         // zh: translation pass
	"Повторить установку": "Retry the installation", // zh: translation pass
	"Проверьте причину ошибки. Для повтора введите SSH-логин и его пароль: root или пользователь с passwordless sudo.": "Check the cause of the error. To retry, enter the SSH login and its password: root or a user with passwordless sudo.", // zh: translation pass
	"Повторно выполнить установку для этой ноды.":                                                                      "Run the installation for this node again.",                                                                            // zh: translation pass
	"Повторить":    "Retry",      // zh: translation pass
	"Открыть ноды": "Open nodes", // zh: translation pass
	"Статус обновляется автоматически каждые 4 секунды.": "The status refreshes every 4 seconds.", // zh: translation pass
	"Открыть список заданий":                             "Open the job list",                     // zh: translation pass
	"Вернуться к заданиям":                               "Back to the jobs",                      // zh: translation pass

	"Новая нода": "New node", // zh: translation pass
	"Мастер установит Mistgate Agent на Ubuntu 22.04+ или Debian 12+ с systemd. Можно войти как root или как SSH-пользователь с passwordless sudo.": "The wizard installs the Mistgate agent on Ubuntu 22.04+ or Debian 12+ with systemd. Log in as root or as an SSH user with passwordless sudo.", // zh: translation pass
	"Адрес SSH-сервера":                 "SSH server address",              // zh: translation pass
	"node.example.com или публичный IP": "node.example.com or a public IP", // zh: translation pass
	"Порт SSH":        "SSH port",        // zh: translation pass
	"Начать проверку": "Start the check", // zh: translation pass
	"Пароль не запрашивается до подтверждения отпечатка SSH host key.": "No password is asked for before you confirm the SSH host key fingerprint.", // zh: translation pass
	"Последние задания": "Recent jobs",   // zh: translation pass
	"Нода":              "Node",          // zh: translation pass
	"Состояние":         "State",         // zh: translation pass
	"Открыть":           "Open",          // zh: translation pass
	"Доступ к серверам": "Server access", // zh: translation pass
	"Пароли хранятся в зашифрованном виде. При смене панель сначала проверит новый вход и только затем заменит сохранённый пароль.": "Passwords are stored encrypted. On a change the panel checks the new login first and only then replaces the saved password.", // zh: translation pass
	"Подключение":    "Login",           // zh: translation pass
	"Сменить пароль": "Change password", // zh: translation pass
	"Смена ожидает проверки; повторите её для восстановления.":                                                         "A change waits for its check; repeat it to recover.",                                                               // zh: translation pass
	"Нода выведена из флота: панель больше не меняет этот сервер. Пароль можно показать или забыть в настройках ноды.": "The node is retired: the panel no longer changes this server. Show or forget the password in the node's settings.", // zh: translation pass
	"Новый пароль SSH":            "New SSH password",                // zh: translation pass
	"Сменить пароль пользователя": "Change the password of the user", // zh: translation pass
	"Сменить":                     "Change",                          // zh: translation pass
	"Изменения на сервере начинаются только после успешной проверки требований, подтверждения ключа и нажатия «Запустить установку».": "Nothing on the server changes until the requirements check passes, the key is confirmed and you press “Start the installation”.", // zh: translation pass

	// messages
	"Форма повреждена или слишком велика. Начните проверку заново.":                                                                              "The form is damaged or too large. Start the check again.",                                                                                          // zh: translation pass
	"Не удалось загрузить задание. Попробуйте ещё раз.":                                                                                          "Could not load the job. Try again.",                                                                                                                // zh: translation pass
	"Задание не найдено. Возможно, оно уже удалено.":                                                                                             "Job not found. It may have been deleted.",                                                                                                          // zh: translation pass
	"Не удалось загрузить события задания.":                                                                                                      "Could not load the job's events.",                                                                                                                  // zh: translation pass
	"Задание отменено до подключения к серверу; временные SSH-данные удалены.":                                                                   "The job was cancelled before it connected to the server; the temporary SSH data is deleted.",                                                       // zh: translation pass
	"Установка остановлена. Проверьте сервер: часть SSH-команд могла уже выполниться.":                                                           "The installation was stopped. Check the server: some SSH commands may already have run.",                                                           // zh: translation pass
	"Не удалось загрузить задания установки.":                                                                                                    "Could not load the installation jobs.",                                                                                                             // zh: translation pass
	"Пароль изменён, новый SSH-вход проверен.":                                                                                                   "Password changed, the new SSH login is verified.",                                                                                                  // zh: translation pass
	"Не удалось загрузить список SSH-доступов.":                                                                                                  "Could not load the SSH access list.",                                                                                                               // zh: translation pass
	"Укажите корректный адрес и порт SSH.":                                                                                                       "Enter a valid SSH address and port.",                                                                                                               // zh: translation pass
	"Не удалось получить SSH-отпечаток. Проверьте публичный адрес и доступность порта.":                                                          "Could not read the SSH fingerprint. Check the public address and that the port is reachable.",                                                      // zh: translation pass
	"Сначала получите и подтвердите отпечаток SSH host key.":                                                                                     "First read and confirm the SSH host key fingerprint.",                                                                                              // zh: translation pass
	"Данные SSH-сервера недействительны. Начните проверку заново.":                                                                               "The SSH server data is not valid. Start the check again.",                                                                                          // zh: translation pass
	"SSH-проверка не прошла. Проверьте логин и пароль, ключ сервера и требования к системе.":                                                     "The SSH check failed. Check the login and password, the server key and the system requirements.",                                                   // zh: translation pass
	"Неизвестное действие. Начните установку заново.":                                                                                            "Unknown action. Start the installation again.",                                                                                                     // zh: translation pass
	"Выберите задание и подтвердите отмену.":                                                                                                     "Choose a job and confirm the cancellation.",                                                                                                        // zh: translation pass
	"Не удалось отменить установку. Обновите страницу и проверьте состояние задания.":                                                            "Could not cancel the installation. Reload the page and check the job's state.",                                                                     // zh: translation pass
	"Перед запуском подтвердите ключ сервера и установку.":                                                                                       "Confirm the server key and the installation before starting.",                                                                                      // zh: translation pass
	"Повторная SSH-проверка не прошла. Проверьте пароль и попробуйте снова.":                                                                     "The repeated SSH check failed. Check the password and try again.",                                                                                  // zh: translation pass
	"Не удалось создать задание. Проверьте имя ноды и повторите действие.":                                                                       "Could not create the job. Check the node name and try again.",                                                                                      // zh: translation pass
	"Для повтора выберите задание и подтвердите запуск.":                                                                                         "To retry, choose a job and confirm the start.",                                                                                                     // zh: translation pass
	"Не удалось повторить установку. Проверьте логин, пароль и состояние задания.":                                                               "Could not retry the installation. Check the login, the password and the job's state.",                                                              // zh: translation pass
	"Подтвердите смену пароля и укажите новый пароль не короче 12 символов.":                                                                     "Confirm the change and enter a new password of at least 12 characters.",                                                                            // zh: translation pass
	"Не удалось подтвердить смену пароля. Доступ сохранён для восстановления.":                                                                   "Could not verify the password change. The access is kept for recovery.",                                                                            // zh: translation pass
	"Требуется свежая проверка passkey. Войдите в панель и повторите действие.":                                                                  "A fresh passkey check is needed. Sign in to the panel and repeat the action.",                                                                      // zh: translation pass
	"Сессия панели завершилась. Войдите снова и повторите действие.":                                                                             "The panel session ended. Sign in again and repeat the action.",                                                                                     // zh: translation pass
	"Имя ноды уже используется. Выберите другое имя.":                                                                                            "The node name is taken. Choose another one.",                                                                                                       // zh: translation pass
	"Панель не получила ответ от SSH-сервера вовремя. Проверьте порт и firewall сервера или хостера, разрешив TCP-подключение с сервера панели.": "The panel got no answer from the SSH server in time. Check the port and the server's or the hoster's firewall: allow TCP from the panel's server.", // zh: translation pass
	"Сервер не подходит для установки или настройка панели не завершена. Проверьте требования к системе.":                                        "The server does not meet the requirements, or the panel's setup is not finished. Check the system requirements.",                                   // zh: translation pass
	"Проверьте адрес, порт, логин, пароль и данные новой ноды.":                                                                                  "Check the address, port, login, password and the new node's data.",                                                                                 // zh: translation pass

	// job states, phases and events
	"В очереди":                          "Queued",                                     // zh: translation pass
	"Установка":                          "Installing",                                 // zh: translation pass
	"Остановка":                          "Stopping",                                   // zh: translation pass
	"Отменено":                           "Cancelled",                                  // zh: translation pass
	"Нода подключена":                    "Node connected",                             // zh: translation pass
	"Ошибка":                             "Error",                                      // zh: translation pass
	"Состояние неизвестно":               "Unknown state",                              // zh: translation pass
	"Ожидает запуска":                    "Waiting to start",                           // zh: translation pass
	"Проверка сервера":                   "Checking the server",                        // zh: translation pass
	"Настройка firewall сервера":         "Setting up the server firewall",             // zh: translation pass
	"Передача агента":                    "Transferring the agent",                     // zh: translation pass
	"Регистрация ноды":                   "Registering the node",                       // zh: translation pass
	"Установка службы":                   "Installing the service",                     // zh: translation pass
	"Ожидание подключения":               "Waiting for the connection",                 // zh: translation pass
	"Завершено":                          "Done",                                       // zh: translation pass
	"Остановка SSH-задания":              "Stopping the SSH job",                       // zh: translation pass
	"Завершилось с ошибкой":              "Ended with an error",                        // zh: translation pass
	"Подготовка":                         "Preparing",                                  // zh: translation pass
	"Задание поставлено в очередь":       "The job is queued",                          // zh: translation pass
	"Запрошен повтор установки":          "A retry was requested",                      // zh: translation pass
	"Владелец запросил отмену установки": "The owner asked to cancel the installation", // zh: translation pass
	"Задание отменено до подключения к серверу":                 "The job was cancelled before it connected to the server", // zh: translation pass
	"SSH-работа остановлена; состояние сервера нужно проверить": "The SSH work was stopped; check the server's state",      // zh: translation pass
	"Проверка сервера и SSH-ключа":                              "Checking the server and the SSH key",                     // zh: translation pass
	"Настройка активного firewall сервера":                      "Setting up the server's active firewall",                 // zh: translation pass
	"Передача агента на сервер":                                 "Transferring the agent to the server",                    // zh: translation pass
	"Регистрация ноды в панели":                                 "Registering the node in the panel",                       // zh: translation pass
	"Настройка и запуск systemd-службы":                         "Setting up and starting the systemd service",             // zh: translation pass
	"Ожидание подключения агента":                               "Waiting for the agent to connect",                        // zh: translation pass
	"Нода подключилась к панели":                                "The node connected to the panel",                         // zh: translation pass

	// job errors
	"SSH-задание остановлено. Проверьте сервер: часть команд установки могла уже выполниться.":                                           "The SSH job was stopped. Check the server: some installation commands may already have run.",                                                        // zh: translation pass
	"Укажите публичный адрес сервера, доступный по SSH.":                                                                                 "Enter the server's public address that SSH can reach.",                                                                                              // zh: translation pass
	"Ключ SSH изменился после подтверждения. Установка остановлена.":                                                                     "The SSH key changed after you confirmed it. The installation is stopped.",                                                                           // zh: translation pass
	"Сервер отклонил SSH-логин или пароль. Проверьте их и повторите установку.":                                                          "The server refused the SSH login or password. Check them and retry the installation.",                                                               // zh: translation pass
	"Сервер не принимает вход по паролю, только по ключу. Поставьте ноду командой вручную или временно разрешите вход по паролю в sshd.": "The server does not accept password logins, only keys. Install the node with the manual command, or allow password logins in sshd for the install.", // zh: translation pass
	refusedTargetMessage: "The panel's network refused the connection to this address, or the server cannot be reached from it. The panel connects from Cloudflare's network: Cloudflare addresses, names behind Cloudflare's proxy (orange cloud) and private networks are unreachable. Enter the server's own public address, or install the node with the manual command (in the panel: add a node, \"Get a manual install command\").", // zh: translation pass
	edgeTimeoutMessage:   "The panel got no answer from the SSH server in time. The panel connects from Cloudflare's network, not from your computer: SSH must accept inbound TCP from the internet (the hoster's and the server's firewall), not only from your IP. Open it for the install, or install the node with the manual command.",                                                                                                // zh: translation pass
	edgeTimeoutLabel:     "The server did not answer in time. The panel connects from Cloudflare's network, not from your computer: SSH must accept inbound TCP from the internet, not only from your IP.",                                                                                                                                                                                                                                 // zh: translation pass
	"Сервер не ответил вовремя. Проверьте сеть и порт SSH.":                             "The server did not answer in time. Check the network and the SSH port.",                         // zh: translation pass
	"Поддерживаются Ubuntu 22.04+ и Debian 12+.":                                        "Ubuntu 22.04+ and Debian 12+ are supported.",                                                    // zh: translation pass
	"Поддерживаются архитектуры amd64 и arm64.":                                         "The amd64 and arm64 architectures are supported.",                                               // zh: translation pass
	"На сервере не найден systemd.":                                                     "The server has no systemd.",                                                                     // zh: translation pass
	"Сервер не может подключиться к панели. Проверьте адрес панели и исходящий доступ.": "The server cannot reach the panel. Check the panel's address and the server's outbound access.", // zh: translation pass
	"На сервере недостаточно процессорных ресурсов или памяти.":                         "The server has too little CPU or memory.",                                                       // zh: translation pass
	"На сервере недостаточно свободного места для агента.":                              "The server has too little free disk space for the agent.",                                       // zh: translation pass
	"Агент установлен, но не подключился к панели вовремя.":                             "The agent is installed but did not connect to the panel in time.",                               // zh: translation pass
	"SSH-подключение прервано. Проверьте сервер и повторите установку.":                 "The SSH connection broke. Check the server and retry the installation.",                         // zh: translation pass
	"Не удалось подключиться к SSH-серверу. Проверьте адрес и порт.":                    "Could not connect to the SSH server. Check the address and the port.",                           // zh: translation pass
	"Не удалось завершить предварительную проверку сервера.":                            "The server check could not finish.",                                                             // zh: translation pass
	"На сервере обнаружена другая или повреждённая регистрация Mistgate.":               "The server has another or a damaged Mistgate registration.",                                     // zh: translation pass
	"Панели не удалось безопасно продолжить это задание. Проверьте журналы панели.":     "The panel could not safely continue this job. Check the panel's logs.",                          // zh: translation pass
	"Эта нода была выведена из эксплуатации и не может быть зарегистрирована повторно.": "This node was retired and cannot be registered again.",                                          // zh: translation pass
	"Не удалось настроить активный firewall на сервере. Проверьте права SSH-пользователя и правила UFW/firewalld, затем повторите установку. Firewall хостера настраивается отдельно.": "Could not set up the server's active firewall. Check the SSH user's rights and the UFW/firewalld rules, then retry. The hoster's firewall is set up separately.", // zh: translation pass
	"В доверенном наборе релиза не найден агент для этой архитектуры.":                                                                                                                 "The trusted release bundle has no agent for this architecture.",                                                                                                  // zh: translation pass
	"Не удалось передать агент на сервер.":                 "Could not transfer the agent to the server.",      // zh: translation pass
	"Не удалось безопасно зарегистрировать ноду в панели.": "Could not safely register the node in the panel.", // zh: translation pass
	"Имя уже используется. Укажите другое имя ноды.":       "The name is taken. Choose another node name.",     // zh: translation pass
	"Не удалось установить или запустить службу агента.":   "Could not install or start the agent service.",    // zh: translation pass
	"Установка остановлена":                                "The installation stopped",                         // zh: translation pass
}
