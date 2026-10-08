// The UDP delivery check (design/udp-port-check.md): the "UDP ports" block on a node, the refusal and the warnings of
// CreateInbound / UpdateInbound / UpdateProfile, the twin's per-node badges, the event. English is the source of truth: `ru`
// is type-checked against it. The admin speaks «ты». "a|b" is a plural.
export const en = {
  // the panel's codes (src/lib/errors.ts errorCodes); {when} and {sender} are worded by lossyVars (src/lib/port-check.ts)
  "err.port_lossy": "Port {port} loses {lost} % of UDP packets on {node} (checked from {sender}, {when}).",
  "err.no_clean_port": "No port without UDP loss was found on all the nodes of this profile. Try again later; a node’s Profiles tab shows which of its ports lose packets.",

  "ports.senderPanel": "the panel",
  "ports.whenUnknown": "earlier",

  // the block on the node's Profiles tab
  "ports.title": "UDP ports",
  "ports.intro": "Another node sends test packets to this node’s ports and counts how many arrive. A port that loses packets makes a profile slow or drop.",
  "ports.never": "Not checked yet. Check the ports to see whether UDP reaches them.",
  "ports.check": "Check ports",
  "ports.checking": "Checking…",
  "ports.checkingNote": "About 8 seconds: test packets go to the node’s ports.",
  "ports.offline": "The node is offline: a check needs its agent.",
  "ports.ok": "OK",
  "ports.lossy": "Loses {lost} %",
  "ports.broken": "{lost} % lost",
  "ports.unchecked": "Not checked",
  "ports.row.arrived": "{got} of {sent} arrived",
  "ports.row.from": "from {sender}",
  "ports.row.cleanNow": "Lost packets {when}, clean now.",
  "ports.row.noProfile": "no profile on it",

  // why a check could not run: the one-line explanation (PortCheck.reason / CheckPortsResponse.error_code)
  "ports.why.node_offline": "The node is offline: a check needs its agent to be connected.",
  "ports.why.agent_too_old": "The agent on this node is too old for the check. Update it on the Updates page.",
  "ports.why.no_sender": "No other node can send the test packets. A second node can check this one.",
  "ports.why.busy": "A check of this node is already running. Try again in a minute.",
  "ports.why.inconclusive": "Inconclusive: even the best port got only {best} % of the packets, so the path from the sender is lossy and says nothing about single ports. Try again later.",
  "ports.why.same_host": "The panel and the node are on one host: the packets never left the machine, so nothing was saved.",
  "ports.why.no_route": "The sender has no route to this node’s address.",
  "ports.why.failed": "The check failed. Try again; if it repeats, the node’s Logs tab may say why.",

  // the same reasons, short, for a badge ("unchecked: no sender")
  "ports.short.node_offline": "node offline",
  "ports.short.agent_too_old": "agent too old",
  "ports.short.no_sender": "no sender",
  "ports.short.busy": "busy",
  "ports.short.inconclusive": "inconclusive",
  "ports.short.same_host": "same host",
  "ports.short.no_route": "no route",
  "ports.short.failed": "failed",

  // add / change a profile on a node
  "ports.anyway.add": "Add anyway",
  "ports.anyway.save": "Save anyway",
  "ports.anyway.put": "Put on anyway",
  "ports.auto": "Port {from} loses {lost} % of UDP packets here: {to} is filled in instead. You can type your own.",
  "ports.saving": "Checking UDP to port {port}…",
  "ports.unchecked.saved": "Port {port} on {node} was not checked for UDP loss. {why}",

  // put a profile on nodes
  "ports.deploy.free": "Port {free} is clean.",

  // the twin
  "twin.checking": "Checking UDP on {n} node…|Checking UDP on {n} nodes…",
  "twin.udp": "UDP",
  "twin.udp.ok": "{node}: checked, no loss",
  "twin.udp.lossy": "{node}: loses {lost} %",
  "twin.udp.unchecked": "{node}: unchecked ({why})",
  "twin.udp.skipped": "{port} skipped: lost {lost} % on {node}",

  // the event
  "event.port_lossy": "UDP to port {port} of “{profile}” loses {lost} % of packets (checked from {sender})",

  // the audit trail
  "audit.node.ports_check": "checked UDP delivery on {node}: ports {ports}",
  "audit.port_lossy_override": "saved the port {port} on {node} although it loses UDP packets",
};

export const ru: typeof en = {
  "err.port_lossy": "Порт {port} теряет {lost} % UDP-пакетов на {node} (проверка с {sender}, {when}).",
  "err.no_clean_port": "Не нашёл порт без потерь UDP сразу на всех нодах профиля. Попробуй позже; на вкладке «Профили» ноды видно, какие её порты теряют пакеты.",

  "ports.senderPanel": "панели",
  "ports.whenUnknown": "ранее",

  "ports.title": "UDP-порты",
  "ports.intro": "Другая нода шлёт на порты этой ноды тестовые пакеты и считает, сколько дошло. Порт с потерями делает профиль медленным или рвёт соединение.",
  "ports.never": "Ещё не проверялось. Проверь порты — так видно, доходит ли до них UDP.",
  "ports.check": "Проверить порты",
  "ports.checking": "Проверяю…",
  "ports.checkingNote": "Около 8 секунд: на порты ноды идут тестовые пакеты.",
  "ports.offline": "Нода не на связи — для проверки нужен её агент.",
  "ports.ok": "Чисто",
  "ports.lossy": "Теряет {lost} %",
  "ports.broken": "Потеряно {lost} %",
  "ports.unchecked": "Не проверен",
  "ports.row.arrived": "дошло {got} из {sent}",
  "ports.row.from": "с {sender}",
  "ports.row.cleanNow": "Пакеты терялись {when}, сейчас чисто.",
  "ports.row.noProfile": "профиля на нём нет",

  "ports.why.node_offline": "Нода не на связи — для проверки нужен подключённый агент.",
  "ports.why.agent_too_old": "Агент на этой ноде слишком старый для проверки. Обнови его на странице «Обновления».",
  "ports.why.no_sender": "Тестовые пакеты некому слать. Проверить эту ноду сможет вторая нода.",
  "ports.why.busy": "Эту ноду уже проверяют. Попробуй через минуту.",
  "ports.why.inconclusive": "Неясно: даже лучший порт получил лишь {best} % пакетов — путь от отправителя теряет сам, и про отдельные порты это ничего не говорит. Попробуй позже.",
  "ports.why.same_host": "Панель и нода на одном сервере: пакеты не выходили из машины, поэтому ничего не сохранено.",
  "ports.why.no_route": "У отправителя нет пути до адреса этой ноды.",
  "ports.why.failed": "Проверка не удалась. Попробуй ещё раз; если повторится, причину может подсказать вкладка «Логи» ноды.",

  "ports.short.node_offline": "нода не на связи",
  "ports.short.agent_too_old": "агент старый",
  "ports.short.no_sender": "некому слать",
  "ports.short.busy": "занято",
  "ports.short.inconclusive": "неясно",
  "ports.short.same_host": "один сервер",
  "ports.short.no_route": "нет пути",
  "ports.short.failed": "сбой",

  "ports.anyway.add": "Всё равно добавить",
  "ports.anyway.save": "Всё равно сохранить",
  "ports.anyway.put": "Всё равно поставить",
  "ports.auto": "Порт {from} теряет {lost} % UDP-пакетов — подставили {to}. Можно вписать свой.",
  "ports.saving": "Проверяю UDP до порта {port}…",
  "ports.unchecked.saved": "Порт {port} на {node} не проверен на потери UDP. {why}",

  "ports.deploy.free": "Порт {free} без потерь.",

  "twin.checking": "Проверяю UDP на {n} ноде…|Проверяю UDP на {n} нодах…|Проверяю UDP на {n} нодах…",
  "twin.udp": "UDP",
  "twin.udp.ok": "{node}: проверен, потерь нет",
  "twin.udp.lossy": "{node}: теряет {lost} %",
  "twin.udp.unchecked": "{node}: не проверен ({why})",
  "twin.udp.skipped": "{port} пропущен: потеряно {lost} % на {node}",

  "event.port_lossy": "UDP до порта {port} профиля «{profile}» теряет {lost} % пакетов (проверка с {sender})",

  "audit.node.ports_check": "проверил(а) доставку UDP на {node}: порты {ports}",
  "audit.port_lossy_override": "сохранил(а) порт {port} на {node}, хотя он теряет UDP-пакеты",
};
