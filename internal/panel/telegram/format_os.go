package telegram

// The alert os_update_failed: an OS update of one node (UpdateService.StartOsRollout) stopped. The reason is the step's error key
// (the admin's os.step.err.*), then, where the node gave one, its code (os.job.err.*), then what to do. Nothing in the text
// offers a reboot that the node refused: the checks of the node are final.

var osUpdateReasons = map[string]localizedText{
	"agent_refused":       {en: "the node did not start the job", ru: "нода не запустила задание", zh: "节点没有启动该任务"},
	"job_failed":          {en: "the job failed", ru: "задание не выполнилось", zh: "任务失败"},
	"reboot_held_only":    {en: "the node was not rebooted: it refused", ru: "ноду не перезагрузили: она отказалась", zh: "节点没有重启：它拒绝了"},
	"node_lost":           {en: "the node dropped off while the panel was checking it after the job: look at the server, and open your hoster’s console if it does not answer", ru: "нода пропала из сети, пока панель проверяла её после задания: посмотрите на сервер, а если он не отвечает, откройте консоль хостера", zh: "任务结束后面板检查期间节点掉线了：请查看服务器，如果它没有响应，请打开服务商的控制台"},
	"reboot_held":         {en: "the updates are installed, but the node was not rebooted", ru: "обновления установлены, но нода не перезагружена", zh: "更新已安装，但节点没有重启"},
	"no_answer":           {en: "the node did not answer in time", ru: "нода не ответила вовремя", zh: "节点未在时限内响应"},
	"not_back":            {en: "the node did not come back within 15 minutes after the reboot: open your provider’s console; if it hangs, choose the previous kernel in the GRUB menu", ru: "нода не вернулась за 15 минут после перезагрузки: откройте консоль провайдера, а если она зависла, выберите прошлое ядро в меню GRUB", zh: "节点重启后 15 分钟内没有回来：请打开服务商的控制台，如果卡住，在 GRUB 菜单中选择之前的内核"},
	"kernel_not_switched": {en: "the node started on another kernel than the one installed: check the boot menu (GRUB)", ru: "нода загрузилась на другом ядре, чем установленное: проверьте меню загрузки (GRUB)", zh: "节点启动的内核与已安装的不同：请检查启动菜单（GRUB）"},
	"state_not_applied":   {en: "the node did not apply its configuration after the update", ru: "нода не применила настройки после обновления", zh: "更新后节点没有应用其配置"},
	"inbounds_unreadable": {en: "the panel could not read the node’s profiles", ru: "панель не смогла прочитать профили ноды", zh: "面板未能读取此节点的协议配置"},
	"warp_down":           {en: "WARP did not come up after the update", ru: "WARP не поднялся после обновления", zh: "更新后 WARP 未能启动"},
	"inbound_failed":      {en: "a profile that worked before broke", ru: "сломался профиль, который работал до обновления", zh: "之前正常的协议配置出了故障"},
	"probe_failed":        {en: "the client-eye check failed", ru: "не прошла проверка глазами клиента", zh: "客户端视角检测失败"},
	"job_not_settled":     {en: "the node did not finish checking itself after the reboot", ru: "нода не закончила проверку себя после перезагрузки", zh: "节点重启后没有完成自检"},
}

// osJobCodes are the node's own words for a refusal or a failure (agent.proto OsJob.code), each with what to do.
var osJobCodes = map[string]localizedText{
	"unsupported_os":          {en: "this system cannot be updated from the panel: update it by hand", ru: "эту систему нельзя обновить из панели: обновите её вручную", zh: "此系统无法通过面板更新：请手动更新"},
	"no_systemd":              {en: "the server has no systemd: update it by hand", ru: "на сервере нет systemd: обновите его вручную", zh: "服务器没有 systemd：请手动更新"},
	"bad_request":             {en: "the node did not accept the request: update the node agent", ru: "нода не приняла запрос: обновите агент ноды", zh: "节点未接受该请求：请更新节点 Agent"},
	"busy":                    {en: "another job is running on the server: wait for it to finish", ru: "на сервере уже идёт другое задание: дождитесь его конца", zh: "服务器上已有其他任务在运行：请等待其结束"},
	"disk_full":               {en: "the disk is too full: free some space", ru: "диск слишком заполнен: освободите место", zh: "磁盘空间不足：请释放一些空间"},
	"apt_lock":                {en: "another package manager held the lock too long: try again in a few minutes", ru: "другой менеджер пакетов слишком долго держал блокировку: повторите через несколько минут", zh: "其他包管理器长时间占用锁：请几分钟后重试"},
	"dpkg_broken":             {en: "the package database is broken: run sudo dpkg --configure -a on the server", ru: "база пакетов повреждена: выполните на сервере sudo dpkg --configure -a", zh: "软件包数据库已损坏：请在服务器上运行 sudo dpkg --configure -a"},
	"security_source_failed":  {en: "the server could not reach its security package source: check its mirror and network", ru: "сервер не достучался до источника обновлений безопасности: проверьте его зеркало и сеть", zh: "服务器无法连接其安全更新源：请检查镜像和网络"},
	"refresh_failed":          {en: "the package lists could not be refreshed: check the server’s network and mirrors", ru: "списки пакетов не обновились: проверьте сеть и зеркала сервера", zh: "无法刷新软件包列表：请检查服务器的网络和镜像"},
	"install_failed":          {en: "installing the updates failed: see the job’s log on the server (journalctl -u mistgate-os-update)", ru: "установка обновлений не удалась: смотрите журнал задания на сервере (journalctl -u mistgate-os-update)", zh: "安装更新失败：请查看服务器上的任务日志（journalctl -u mistgate-os-update）"},
	"auto_reboot_conflict":    {en: "unattended-upgrades is set to reboot by itself, which would bypass the checks: turn Automatic-Reboot off", ru: "unattended-upgrades настроен перезагружать сервер сам, это обошло бы проверки: выключите Automatic-Reboot", zh: "unattended-upgrades 被设为自动重启，这会绕过检查：请关闭 Automatic-Reboot"},
	"dpkg_interrupted":        {en: "a package installation was interrupted: run sudo dpkg --configure -a on the server", ru: "установка пакета была прервана: выполните на сервере sudo dpkg --configure -a", zh: "软件包安装被中断：请在服务器上运行 sudo dpkg --configure -a"},
	"awg_module_missing":      {en: "the AmneziaWG module is not built for the new kernel, so the node was not rebooted: build the module first", ru: "модуль AmneziaWG не собран под новое ядро, поэтому ноду не перезагрузили: сначала соберите модуль", zh: "AmneziaWG 模块尚未为新内核构建，因此节点没有重启：请先构建模块"},
	"boot_full":               {en: "the /boot partition is too full for the new kernel: remove old kernels", ru: "раздел /boot слишком заполнен для нового ядра: удалите старые ядра", zh: "/boot 分区空间不足以容纳新内核：请删除旧内核"},
	"initramfs_missing":       {en: "the files that start the new kernel are missing, so the node was not rebooted: reinstall the kernel package", ru: "нет файлов для запуска нового ядра, поэтому ноду не перезагрузили: переустановите пакет ядра", zh: "缺少启动新内核的文件，因此节点没有重启：请重新安装内核包"},
	"reboot_failed":           {en: "the reboot command was refused or the server did not go down: reboot it from your provider’s panel", ru: "команда перезагрузки отклонена или сервер не выключился: перезагрузите его из панели провайдера", zh: "重启命令被拒绝或服务器没有关机：请在服务商面板中重启"},
	"timeout":                 {en: "the job took too long and was stopped", ru: "задание шло слишком долго и было остановлено", zh: "任务耗时过长，已被停止"},
	"interrupted":             {en: "the job stopped without a result: check the server", ru: "задание оборвалось без результата: проверьте сервер", zh: "任务无结果地中断了：请检查服务器"},
	"cancelled":               {en: "the job was cancelled before it changed anything", ru: "задание отменили, ничего не изменив", zh: "任务在改动任何内容之前被取消"},
	"launch_failed":           {en: "the job could not be started: check the node’s disk and the agent’s log", ru: "задание не удалось запустить: проверьте диск ноды и журнал агента", zh: "无法启动任务：请检查节点磁盘和 Agent 日志"},
	"kernel_not_switched":     {en: "the server restarted on another kernel than the one installed: check the boot menu (GRUB)", ru: "сервер загрузился на другом ядре, чем установленное: проверьте меню загрузки (GRUB)", zh: "服务器启动的内核与已安装的不同：请检查启动菜单（GRUB）"},
	"awg_module_not_loaded":   {en: "after the reboot the AmneziaWG module did not load: rebuild it or switch the backend to Auto", ru: "после перезагрузки модуль AmneziaWG не загрузился: пересоберите его или переключите бэкенд на Auto", zh: "重启后 AmneziaWG 模块没有加载：请重新构建，或将后端切换为 Auto"},
	"inbound_failed":          {en: "after the reboot some profiles did not start: see the node’s Profiles tab", ru: "после перезагрузки часть профилей не запустилась: смотрите вкладку «Профили» ноды", zh: "重启后部分协议配置没有启动：请查看节点的“配置”标签页"},
	"kernel_unknown":          {en: "the kernel the server will boot could not be determined, so it was not rebooted", ru: "не удалось определить, какое ядро загрузится, поэтому сервер не перезагрузили", zh: "无法确定服务器将启动的内核，因此没有重启"},
	"kernel_older":            {en: "the next boot would start an older kernel, so it was not rebooted: check the boot menu (GRUB)", ru: "при следующей загрузке запустилось бы ядро старше текущего, поэтому ноду не перезагрузили: проверьте меню загрузки (GRUB)", zh: "下次启动会使用比当前更旧的内核，因此没有重启：请检查启动菜单（GRUB）"},
	"awg_module_build_failed": {en: "the AmneziaWG module failed to build for the new kernel; the node was not rebooted and the loaded module keeps working: fix the build first", ru: "модуль AmneziaWG не собрался под новое ядро; ноду не перезагрузили, загруженный модуль работает: сначала исправьте сборку", zh: "AmneziaWG 模块无法为新内核构建；节点没有重启，已加载的模块继续工作：请先修复构建"},
	"too_soon":                {en: "the server restarted less than 10 minutes ago, so it was not rebooted again", ru: "сервер перезагружался меньше 10 минут назад, поэтому повторно его не перезагрузили", zh: "服务器 10 分钟内刚重启过，因此没有再次重启"},
	"boot_entry_missing":      {en: "the boot menu (GRUB) has no entry for the newest kernel, so the node was not rebooted: run update-grub on the server or reboot from your provider’s panel", ru: "в меню загрузки (GRUB) нет пункта для самого нового ядра, поэтому ноду не перезагрузили: выполните update-grub на сервере или перезагрузите из панели провайдера", zh: "启动菜单（GRUB）中没有最新内核的条目，因此节点没有重启：请在服务器上运行 update-grub，或在服务商面板中重启"},
	"boot_default_custom":     {en: "the boot menu (GRUB) is set to a chosen or saved entry, not the newest kernel, so the node was not rebooted: set GRUB_DEFAULT=0 or reboot from your provider’s panel", ru: "меню загрузки (GRUB) настроено на выбранный или сохранённый пункт, а не на новейшее ядро, поэтому ноду не перезагрузили: поставьте GRUB_DEFAULT=0 или перезагрузите из панели провайдера", zh: "启动菜单（GRUB）被设为指定或已保存的条目，而不是最新内核，因此节点没有重启：请设置 GRUB_DEFAULT=0，或在服务商面板中重启"},
	"reboot_limit":            {en: "the panel already rebooted this server within the last hour or three times today, so it will not do it again yet", ru: "панель уже перезагружала этот сервер в последний час или трижды за сутки, поэтому пока не будет делать это снова", zh: "面板在最近一小时内或今天已三次重启过此服务器，因此暂不再重启"},
	"unknown":                 {en: "the node reported a problem the panel does not know: see its History tab", ru: "нода сообщила о проблеме, которой панель не знает: смотрите её историю событий", zh: "节点报告了面板不认识的问题：请查看其历史记录"},
}

// osUpdateReason is the sentence under the title of an os_update_failed alert: the step's reason, then the node's code with what to do.
func osUpdateReason(l L, p map[string]string) string {
	key := p["reason"]
	if key == "reboot_held" && p["mode"] == "reboot_only" { // nothing was installed: only the reboot was asked for
		key = "reboot_held_only"
	}
	reason, ok := osUpdateReasons[key]
	text := ""
	if ok {
		text = upperFirst(reason.forLanguage(l)) + l.stop()
	}
	// the reason that already carries its own advice (not_back, kernel_not_switched) needs no second sentence for the same thing
	if code, ok := osJobCodes[p["code"]]; ok && p["reason"] != "not_back" && p["reason"] != "kernel_not_switched" {
		if text != "" {
			text += l.gap()
		}
		text += upperFirst(code.forLanguage(l)) + l.stop()
	}
	if text == "" {
		return data(p["reason"], 80)
	}
	return text
}

var osUpdateTitle = localizedText{en: "The OS update of a node stopped", ru: "Обновление ОС ноды остановилось", zh: "节点的系统更新已停止"}
