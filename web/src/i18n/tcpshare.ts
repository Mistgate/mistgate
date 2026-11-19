// Services of one node that share a TCP port (443) and are told apart by the name in the connection (SNI): the panel's refusals when
// a profile cannot stand beside another one there, and its words for what happens when it can. English is the source of truth: the
// other four are type-checked against it. The admin speaks «ты» in Russian. The values {port} {node} {profile} {name} {protocol}
// {free} come from the panel (lib/errors.ts, lib/port-check.ts); {profile} is the name the admin gave the other profile.
export const en = {
  // sni_taken: another service of the port holds the name this one would answer to
  "err.sni_taken":
    "Port {port} on {node} can be shared by several services, which are told apart by the name in the connection (SNI). The name “{name}” already belongs to “{profile}”. Give this profile a name of its own on that node (a subdomain of the node’s address with an A record to the node), or pick another port.",
  "err.sni_taken_free":
    "Port {port} on {node} can be shared by several services, which are told apart by the name in the connection (SNI). The name “{name}” already belongs to “{profile}”. Give this profile a name of its own on that node (a subdomain of the node’s address with an A record to the node), or take {free}, which is free.",
  // share_needs_site: none of the services of the port shows the node's own site to a visitor who sends no name
  "err.share_needs_site":
    "Port {port} on {node} is shared only when one of its services shows the node’s own site to a visitor who sends no name or an unknown one. “{profile}” and this profile show another site. Put a profile with the node’s own site on that port (VLESS REALITY of the node’s own site, TrustTunnel, MTProto with the decoy), or pick another port.",
  // share_unsupported: a service of the port has no name to be told apart by, or is a protocol that cannot share
  "err.share_unsupported":
    "“{profile}” ({protocol}) cannot share port {port} on {node}: it has no host name of its own that a visitor could be told apart by. Give it a domain name, or pick another port.",
  // share_agent_old: the agent of the node cannot share a port yet
  "err.share_agent_old": "Port {port} on {node} is used by “{profile}”, and sharing a port needs a newer agent on the node. Update the node’s agent first, or pick another port.",
  // tcp_port_shared and share_two_faces: the warnings of a profile that does stand beside another (lib/port-check.ts sharedNote)
  "node.check.shared":
    "TCP {port} on {node} is shared by {profiles}. The node tells them apart by the name in the connection; a visitor with no name or an unknown one gets the site of “{default}”.",
  "node.check.sharedRestart": "“{restarts}” restarts, and its open connections reconnect.",
  "node.check.twoFaces":
    "On TCP {port} of {node}, “{profile}” shows another site. A visitor who asks for its name sees that site, one with no name or another name sees the site of “{default}”; someone who tries many names can tell the two apart.",
  "approval.value.warn.tcp_port_shared": "the TCP port is shared with other services",
  "approval.value.warn.share_two_faces": "a service of the shared port shows another site",
  "approval.fact.sni": "Domain (SNI) on the node",
  // the dialogs and the card of a shared TCP port (S5)
  "share.dns.title":
    "DNS record for the name",
  "share.dns.copy":
    "Copy record",
  "share.dns.type":
    "Type",
  "share.dns.name":
    "Name",
  "share.dns.value":
    "Value",
  "share.dns.hint":
    "Add it where the domain’s DNS is kept, as a plain record (no proxy: the grey cloud on Cloudflare). The node asks Let’s Encrypt for the name’s certificate by itself once the record leads to it.",
  "share.dns.hintCname":
    "Add it where the domain’s DNS is kept, with no proxy (the grey cloud on Cloudflare). If your DNS does not take a CNAME here, use an A record with the node’s IPv4 address. The node asks Let’s Encrypt for the certificate by itself once the name leads to it.",
  "share.openUpdates":
    "Open updates",
  "share.title":
    "Put it beside “{profile}” on port {port}",
  "share.body":
    "Both would stand on port {port} of {node}, and the node tells them apart by the name in the connection (SNI). “{profile}” already answers to {held}, so this profile needs a name of its own. The one below is a plain subdomain of the node’s address; change it if you like.",
  "share.bodyIp":
    "Both would stand on port {port} of {node}, and the node tells them apart by the name in the connection (SNI). “{profile}” already answers to {held}, so this profile needs a name of its own. {node} is known by an IP address, so type a domain name you control and point it at the node.",
  "share.name":
    "Name of this profile on {node}",
  "share.nameBad":
    "Not a host name: use letters, digits and hyphens in dot-separated parts, not an IP address.",
  "share.put":
    "Put beside on {port}",
  "share.answersTo":
    "It answers to {name} on that port.",
  "share.row.shared":
    "Stands beside {profiles} on {port}",
  "sp.title":
    "TCP {port} is shared",
  "sp.sub":
    "{n} services answer on this port. The node tells them apart by the name in the connection; a visitor with no name, or one it does not know, gets “{default}”.",
  "sp.default":
    "Default",
  "sp.default.hint":
    "Gets a visitor with no name, an unknown name, or something that is not TLS",
  "sp.col.name":
    "Name",
  "sp.col.got":
    "Connections",
  "sp.off":
    "off",
  "sp.stat.accepted":
    "Accepted",
  "sp.stat.default":
    "To the default",
  "sp.stat.refused":
    "Refused",
  "sp.stat.down":
    "Service down",
  "sp.stat.timeouts":
    "Silent 10 s",
  "sp.stat.evicted":
    "Dropped",
  "sp.hint.accepted":
    "Connections the front took since the agent started",
  "sp.hint.default":
    "Visitors with no name, an unknown one, not TLS or a broken hello: sent to the default service",
  "sp.hint.refused":
    "Closed because the service did not take the connection in time",
  "sp.hint.down":
    "The name was a service that was not running: the visitor went to the default service instead",
  "sp.hint.timeouts":
    "Said nothing for 10 seconds and were closed",
  "sp.hint.evicted":
    "Unfinished handshakes dropped to make room when the port was flooded",
  "sp.since":
    "Counted since the agent started · reported {when}",
  "sp.unknown":
    "Names visitors asked for that no service holds",
  "sp.bind":
    "The node could not take port {port}: {error}. The services on it do not run; the node tries again by itself.",
  "sp.waiting":
    "The node has not reported on this port yet. The counters appear within a few minutes.",
  "sp.old.title":
    "This node cannot share a port yet",
  "sp.old.body":
    "Its agent does not know how to share a TCP port. Only “{default}” runs on port {port}; {others} wait until the agent is updated.",
  "sp.old.go":
    "Open updates",
  "node.inbound.err.shareOld":
    "The node’s agent cannot share a TCP port yet, so this profile is not sent to it. Update the agent and it starts beside the other service.",
  "node.inbound.err.shadowed":
    "Another profile on this port holds the name this one answers to, so it does not run. Give it a name of its own.",
  "event.front_overloaded":
    "Port {port} was flooded with unfinished handshakes: the front dropped {evicted} of them to stay up. The services on the port kept running.",
  // the dialogs and the card of a shared TCP port (S5)
  "share.row.held":
    "The name {name} is held by “{profile}”",
  // the dialogs and the card of a shared TCP port (S5)
  "share.dnsNext":
    "The DNS record this name needs is shown next, before you save.",
};

export const ru: typeof en = {
  "err.sni_taken":
    "Порт {port} на {node} могут делить несколько сервисов: их различают по имени в соединении (SNI). Имя «{name}» уже занято профилем «{profile}». Задай этому профилю своё имя на этой ноде (поддомен адреса ноды с A-записью на ноду) или выбери другой порт.",
  "err.sni_taken_free":
    "Порт {port} на {node} могут делить несколько сервисов: их различают по имени в соединении (SNI). Имя «{name}» уже занято профилем «{profile}». Задай этому профилю своё имя на этой ноде (поддомен адреса ноды с A-записью на ноду) или возьми свободный порт {free}.",
  "err.share_needs_site":
    "Порт {port} на {node} можно делить, только если один из сервисов показывает собственный сайт ноды тому, кто пришёл без имени или с неизвестным. «{profile}» и этот профиль показывают чужой сайт. Поставь на этот порт профиль с сайтом самой ноды (VLESS REALITY с сайтом ноды, TrustTunnel, MTProto с заглушкой) или выбери другой порт.",
  "err.share_unsupported":
    "«{profile}» ({protocol}) не может делить порт {port} на {node}: у него нет своего доменного имени, по которому его можно отличить. Задай ему доменное имя или выбери другой порт.",
  "err.share_agent_old": "Порт {port} на {node} занят профилем «{profile}», а чтобы делить порт, на ноде нужен более новый агент. Сначала обнови агент ноды или выбери другой порт.",
  "node.check.shared":
    "TCP {port} на {node} делят {profiles}. Нода различает их по имени в соединении; кто пришёл без имени или с неизвестным, получает сайт «{default}».",
  "node.check.sharedRestart": "«{restarts}» перезапустится, его открытые соединения переподключатся.",
  "node.check.twoFaces":
    "На TCP {port} ноды {node} «{profile}» показывает чужой сайт. Кто спросит его имя, увидит этот сайт; кто придёт без имени или с другим, увидит сайт «{default}». Тот, кто переберёт много имён, сможет отличить эти два сайта.",
  "approval.value.warn.tcp_port_shared": "TCP-порт делят с другими сервисами",
  "approval.value.warn.share_two_faces": "один из сервисов общего порта показывает чужой сайт",
  "approval.fact.sni": "Домен (SNI) на ноде",
  // the dialogs and the card of a shared TCP port (S5)
  "share.dns.title":
    "DNS-запись для имени",
  "share.dns.copy":
    "Копировать запись",
  "share.dns.type":
    "Тип",
  "share.dns.name":
    "Имя",
  "share.dns.value":
    "Значение",
  "share.dns.hint":
    "Добавь её там, где хранится DNS домена, обычной записью без проксирования (на Cloudflare — серое облако). Сертификат для имени нода запросит у Let’s Encrypt сама, когда запись начнёт вести на неё.",
  "share.dns.hintCname":
    "Добавь её там, где хранится DNS домена, без проксирования (на Cloudflare — серое облако). Если DNS не принимает здесь CNAME, поставь A-запись с IPv4-адресом ноды. Сертификат нода запросит у Let’s Encrypt сама, когда имя начнёт вести на неё.",
  "share.openUpdates":
    "Открыть обновления",
  "share.title":
    "Поставить рядом с «{profile}» на порту {port}",
  "share.body":
    "Оба встанут на порт {port} ноды {node}, а нода различает их по имени в соединении (SNI). «{profile}» уже отвечает на {held}, поэтому этому профилю нужно своё имя. Ниже — обычный поддомен адреса ноды; можешь заменить.",
  "share.bodyIp":
    "Оба встанут на порт {port} ноды {node}, а нода различает их по имени в соединении (SNI). «{profile}» уже отвечает на {held}, поэтому этому профилю нужно своё имя. Нода {node} известна по IP-адресу, поэтому впиши доменное имя, которым управляешь, и направь его на ноду.",
  "share.name":
    "Имя этого профиля на {node}",
  "share.nameBad":
    "Это не имя хоста: буквы, цифры и дефисы в частях через точку, не IP-адрес.",
  "share.put":
    "Поставить рядом на {port}",
  "share.answersTo":
    "На этом порту он отвечает на {name}.",
  "share.row.shared":
    "Встанет рядом: {profiles}, порт {port}",
  "sp.title":
    "TCP {port}: общий порт",
  "sp.sub":
    "На этом порту отвечают сервисы: {n}. Нода различает их по имени в соединении; кто пришёл без имени или с неизвестным, получает «{default}».",
  "sp.default":
    "По умолчанию",
  "sp.default.hint":
    "Получает пришедших без имени, с неизвестным именем или не по TLS",
  "sp.col.name":
    "Имя",
  "sp.col.got":
    "Соединений",
  "sp.off":
    "выключен",
  "sp.stat.accepted":
    "Принято",
  "sp.stat.default":
    "Ушло по умолчанию",
  "sp.stat.refused":
    "Отказано",
  "sp.stat.down":
    "Сервис лежал",
  "sp.stat.timeouts":
    "Молчали 10 с",
  "sp.stat.evicted":
    "Вытеснено",
  "sp.hint.accepted":
    "Соединений, которые фронт принял с запуска агента",
  "sp.hint.default":
    "Пришедшие без имени, с неизвестным именем, не по TLS или с битым hello: ушли сервису по умолчанию",
  "sp.hint.refused":
    "Закрыты, потому что сервис не принял соединение вовремя",
  "sp.hint.down":
    "Имя принадлежало сервису, который не работал: посетитель ушёл сервису по умолчанию",
  "sp.hint.timeouts":
    "Молчали 10 секунд и были закрыты",
  "sp.hint.evicted":
    "Незаконченные рукопожатия, вытесненные, когда порт засыпали соединениями",
  "sp.since":
    "Считается с запуска агента · отчёт {when}",
  "sp.unknown":
    "Имена, которые просили посетители и которых нет ни у одного сервиса",
  "sp.bind":
    "Нода не смогла занять порт {port}: {error}. Сервисы на нём не работают; нода сама попробует снова.",
  "sp.waiting":
    "Нода пока не прислала отчёт по этому порту. Счётчики появятся в течение нескольких минут.",
  "sp.old.title":
    "Эта нода пока не умеет делить порт",
  "sp.old.body":
    "Её агент не умеет делить TCP-порт. На порту {port} работает только «{default}»; {others} ждут обновления агента.",
  "sp.old.go":
    "Открыть обновления",
  "node.inbound.err.shareOld":
    "Агент ноды пока не умеет делить TCP-порт, поэтому этот профиль ему не отправляется. Обнови агент, и профиль запустится рядом с другим сервисом.",
  "node.inbound.err.shadowed":
    "Другой профиль на этом порту держит имя, на которое отвечает этот, поэтому он не запущен. Задай ему своё имя.",
  "event.front_overloaded":
    "Порт {port} засыпали незаконченными рукопожатиями: фронт вытеснил их ({evicted}), чтобы не лечь. Сервисы на порту продолжили работать.",
  // the dialogs and the card of a shared TCP port (S5)
  "share.row.held":
    "Имя {name} занято профилем «{profile}»",
  // the dialogs and the card of a shared TCP port (S5)
  "share.dnsNext":
    "Запись DNS, которая нужна этому имени, покажется следующим шагом, до сохранения.",
};

export const zh: typeof en = {
  "err.sni_taken":
    "{node} 上的端口 {port} 可由多个服务共用，通过连接中的名称（SNI）区分。名称“{name}”已被协议配置“{profile}”占用。请在该节点上为此协议配置设置专属名称（节点地址的子域名，并添加指向该节点的 A 记录），或换一个端口。",
  "err.sni_taken_free":
    "{node} 上的端口 {port} 可由多个服务共用，通过连接中的名称（SNI）区分。名称“{name}”已被协议配置“{profile}”占用。请在该节点上为此协议配置设置专属名称（节点地址的子域名，并添加指向该节点的 A 记录），或使用空闲端口 {free}。",
  "err.share_needs_site":
    "只有当其中一个服务向未发送名称或名称未知的访问者展示节点自己的站点时，{node} 上的端口 {port} 才能共用。“{profile}”和此协议配置展示的都是别人的站点。请在该端口放一个展示节点自身站点的协议配置（使用节点自身站点的 VLESS REALITY、TrustTunnel、带伪装站的 MTProto），或换一个端口。",
  "err.share_unsupported": "“{profile}”（{protocol}）无法共用 {node} 上的端口 {port}：它没有可用来区分访问者的专属域名。请为它设置域名，或换一个端口。",
  "err.share_agent_old": "{node} 上的端口 {port} 已被“{profile}”使用，而共用端口需要节点上的新版代理。请先更新节点代理，或换一个端口。",
  "node.check.shared": "{node} 上的 TCP {port} 由 {profiles} 共用。节点按连接中的名称区分它们；未发送名称或名称未知的访问者会看到“{default}”的站点。",
  "node.check.sharedRestart": "“{restarts}”会重启，其已打开的连接会重新连接。",
  "node.check.twoFaces": "在 {node} 的 TCP {port} 上，“{profile}”展示的是别人的站点。请求它名称的访问者会看到那个站点，未带名称或名称不同的访问者会看到“{default}”的站点；尝试大量名称的人可以分辨出这两个站点。",
  "approval.value.warn.tcp_port_shared": "该 TCP 端口与其他服务共用",
  "approval.value.warn.share_two_faces": "共用端口上的一个服务展示的是别人的站点",
  "approval.fact.sni": "节点上的域名 (SNI)",
  // the dialogs and the card of a shared TCP port (S5)
  "share.dns.title":
    "该名称所需的 DNS 记录",
  "share.dns.copy":
    "复制记录",
  "share.dns.type":
    "类型",
  "share.dns.name":
    "名称",
  "share.dns.value":
    "值",
  "share.dns.hint":
    "请在域名的 DNS 托管处添加为普通记录（不要代理：Cloudflare 上用灰色云朵）。记录指向节点后，节点会自行向 Let’s Encrypt 申请该名称的证书。",
  "share.dns.hintCname":
    "请在域名的 DNS 托管处添加，不要代理（Cloudflare 上用灰色云朵）。如果这里的 DNS 不接受 CNAME，请改用指向节点 IPv4 地址的 A 记录。名称指向节点后，节点会自行向 Let’s Encrypt 申请证书。",
  "share.openUpdates":
    "打开更新",
  "share.title":
    "与“{profile}”并排放在端口 {port}",
  "share.body":
    "两者都将使用 {node} 的端口 {port}，节点按连接中的名称（SNI）区分它们。“{profile}”已使用 {held}，因此该协议配置需要自己的名称。下面是节点地址的一个普通子域名，可以修改。",
  "share.bodyIp":
    "两者都将使用 {node} 的端口 {port}，节点按连接中的名称（SNI）区分它们。“{profile}”已使用 {held}，因此该协议配置需要自己的名称。{node} 以 IP 地址标识，请填写你可控的域名并将其指向节点。",
  "share.name":
    "此协议配置在 {node} 上的名称",
  "share.nameBad":
    "不是主机名：请使用以点分隔的字母、数字和连字符，不能是 IP 地址。",
  "share.put":
    "并排放在 {port}",
  "share.answersTo":
    "它在该端口上使用名称 {name}。",
  "share.row.shared":
    "将与 {profiles} 共用端口 {port}",
  "sp.title":
    "TCP {port} 为共用端口",
  "sp.sub":
    "此端口上有 {n} 个服务。节点按连接中的名称区分它们；未带名称或名称未知的访问者会得到“{default}”。",
  "sp.default":
    "默认",
  "sp.default.hint":
    "接收未带名称、名称未知或不是 TLS 的访问者",
  "sp.col.name":
    "名称",
  "sp.col.got":
    "连接数",
  "sp.off":
    "已关闭",
  "sp.stat.accepted":
    "已接受",
  "sp.stat.default":
    "转给默认服务",
  "sp.stat.refused":
    "已拒绝",
  "sp.stat.down":
    "服务不可用",
  "sp.stat.timeouts":
    "沉默 10 秒",
  "sp.stat.evicted":
    "被挤出",
  "sp.hint.accepted":
    "自代理启动以来前端接受的连接",
  "sp.hint.default":
    "未带名称、名称未知、不是 TLS 或 hello 损坏的访问者：交给默认服务",
  "sp.hint.refused":
    "因服务未及时接收而关闭的连接",
  "sp.hint.down":
    "该名称的服务未运行：访问者被转给了默认服务",
  "sp.hint.timeouts":
    "10 秒内没有任何数据而被关闭",
  "sp.hint.evicted":
    "端口被大量连接淹没时，为腾出空间而丢弃的未完成握手",
  "sp.since":
    "自代理启动以来的计数 · 上报于 {when}",
  "sp.unknown":
    "访问者请求过、但没有服务持有的名称",
  "sp.bind":
    "节点无法占用端口 {port}：{error}。该端口上的服务未运行；节点会自行重试。",
  "sp.waiting":
    "节点尚未上报此端口的情况。计数会在几分钟内出现。",
  "sp.old.title":
    "此节点暂时无法共用端口",
  "sp.old.body":
    "其代理还不会共用 TCP 端口。端口 {port} 上只运行“{default}”；{others} 要等代理更新后才会启动。",
  "sp.old.go":
    "打开更新",
  "node.inbound.err.shareOld":
    "节点的代理还不会共用 TCP 端口，所以没有把这个协议配置发给它。更新代理后，它会与另一个服务并排启动。",
  "node.inbound.err.shadowed":
    "此端口上的另一个协议配置占用了这个配置使用的名称，所以它没有运行。请为它设置专属名称。",
  "event.front_overloaded":
    "端口 {port} 被大量未完成的握手淹没：前端丢弃了其中 {evicted} 个以保持运行。该端口上的服务仍在运行。",
  // the dialogs and the card of a shared TCP port (S5)
  "share.row.held":
    "名称 {name} 已被“{profile}”占用",
  // the dialogs and the card of a shared TCP port (S5)
  "share.dnsNext":
    "该名称所需的 DNS 记录会在下一步、保存之前显示。",
};

export const fa: typeof en = {
  "err.sni_taken":
    "پورت {port} روی {node} را می‌شود بین چند سرویس تقسیم کرد؛ آن‌ها با نام داخل اتصال (SNI) از هم جدا می‌شوند. نام «{name}» را پروفایل «{profile}» گرفته است. برای این پروفایل روی آن نود نامی جدا بگذار (زیردامنه‌ای از نشانی نود با رکورد A به نود) یا پورت دیگری انتخاب کن.",
  "err.sni_taken_free":
    "پورت {port} روی {node} را می‌شود بین چند سرویس تقسیم کرد؛ آن‌ها با نام داخل اتصال (SNI) از هم جدا می‌شوند. نام «{name}» را پروفایل «{profile}» گرفته است. برای این پروفایل روی آن نود نامی جدا بگذار (زیردامنه‌ای از نشانی نود با رکورد A به نود) یا پورت آزاد {free} را بگیر.",
  "err.share_needs_site":
    "پورت {port} روی {node} فقط وقتی تقسیم می‌شود که یکی از سرویس‌ها سایت خود نود را به بازدیدکننده‌ای که نام نفرستاده یا نامش ناشناخته است نشان دهد. «{profile}» و این پروفایل سایت دیگری را نشان می‌دهند. پروفایلی با سایت خود نود روی آن پورت بگذار (VLESS REALITY با سایت خود نود، TrustTunnel، MTProto با سایت پوششی) یا پورت دیگری انتخاب کن.",
  "err.share_unsupported": "«{profile}» ({protocol}) نمی‌تواند پورت {port} روی {node} را تقسیم کند: نام دامنهٔ خودش را ندارد که با آن از بقیه جدا شود. برایش نام دامنه بگذار یا پورت دیگری انتخاب کن.",
  "err.share_agent_old": "پورت {port} روی {node} را پروفایل «{profile}» گرفته و تقسیم پورت به عامل جدیدتری روی نود نیاز دارد. اول عامل نود را به‌روز کن یا پورت دیگری انتخاب کن.",
  "node.check.shared": "TCP {port} روی {node} بین {profiles} تقسیم می‌شود. نود آن‌ها را با نام داخل اتصال از هم جدا می‌کند؛ بازدیدکننده‌ای که نام نفرستاده یا نامش ناشناخته است سایت «{default}» را می‌بیند.",
  "node.check.sharedRestart": "«{restarts}» دوباره راه‌اندازی می‌شود و اتصال‌های بازش دوباره وصل می‌شوند.",
  "node.check.twoFaces":
    "روی TCP {port} نود {node}، «{profile}» سایت دیگری را نشان می‌دهد. هر که نام آن را بپرسد همان سایت را می‌بیند و هر که بدون نام یا با نامی دیگر بیاید سایت «{default}» را؛ کسی که نام‌های زیادی را امتحان کند می‌تواند این دو سایت را از هم تشخیص دهد.",
  "approval.value.warn.tcp_port_shared": "پورت TCP با سرویس‌های دیگر تقسیم می‌شود",
  "approval.value.warn.share_two_faces": "یکی از سرویس‌های پورت مشترک سایت دیگری را نشان می‌دهد",
  "approval.fact.sni": "دامنه (SNI) روی نود",
  // the dialogs and the card of a shared TCP port (S5)
  "share.dns.title":
    "رکورد DNS برای این نام",
  "share.dns.copy":
    "کپی رکورد",
  "share.dns.type":
    "نوع",
  "share.dns.name":
    "نام",
  "share.dns.value":
    "مقدار",
  "share.dns.hint":
    "آن را همان‌جا که DNS دامنه نگه‌داری می‌شود به‌صورت رکورد ساده (بدون پراکسی: روی Cloudflare ابر خاکستری) اضافه کن. وقتی رکورد به نود برسد، نود خودش گواهی این نام را از Let’s Encrypt می‌گیرد.",
  "share.dns.hintCname":
    "آن را همان‌جا که DNS دامنه نگه‌داری می‌شود و بدون پراکسی (روی Cloudflare ابر خاکستری) اضافه کن. اگر DNS شما اینجا CNAME نمی‌پذیرد، رکورد A با نشانی IPv4 نود بگذار. وقتی نام به نود برسد، نود خودش گواهی را از Let’s Encrypt می‌گیرد.",
  "share.openUpdates":
    "باز کردن به‌روزرسانی‌ها",
  "share.title":
    "کنار «{profile}» روی پورت {port} بگذار",
  "share.body":
    "هر دو روی پورت {port} نود {node} می‌نشینند و نود آن‌ها را با نام داخل اتصال (SNI) از هم جدا می‌کند. «{profile}» همین حالا به {held} پاسخ می‌دهد، پس این پروفایل نام خودش را لازم دارد. نام زیر یک زیردامنهٔ ساده از نشانی نود است؛ هر طور خواستی عوضش کن.",
  "share.bodyIp":
    "هر دو روی پورت {port} نود {node} می‌نشینند و نود آن‌ها را با نام داخل اتصال (SNI) از هم جدا می‌کند. «{profile}» همین حالا به {held} پاسخ می‌دهد، پس این پروفایل نام خودش را لازم دارد. {node} با نشانی IP شناخته می‌شود؛ پس نام دامنه‌ای را که خودت کنترل می‌کنی بنویس و آن را به نود وصل کن.",
  "share.name":
    "نام این پروفایل روی {node}",
  "share.nameBad":
    "این نام میزبان نیست: حرف، رقم و خط تیره در بخش‌هایی با نقطه، نه نشانی IP.",
  "share.put":
    "کنارش روی {port} بگذار",
  "share.answersTo":
    "روی آن پورت به {name} پاسخ می‌دهد.",
  "share.row.shared":
    "کنار {profiles} روی پورت {port} می‌ایستد",
  "sp.title":
    "TCP {port} مشترک است",
  "sp.sub":
    "{n} سرویس روی این پورت پاسخ می‌دهند. نود آن‌ها را با نام داخل اتصال از هم جدا می‌کند؛ بازدیدکننده‌ای که نام نفرستاده یا نامش ناشناخته است «{default}» را می‌گیرد.",
  "sp.default":
    "پیش‌فرض",
  "sp.default.hint":
    "بازدیدکنندهٔ بدون نام، با نام ناشناخته یا غیر TLS را می‌گیرد",
  "sp.col.name":
    "نام",
  "sp.col.got":
    "اتصال‌ها",
  "sp.off":
    "خاموش",
  "sp.stat.accepted":
    "پذیرفته‌شده",
  "sp.stat.default":
    "به پیش‌فرض رفته",
  "sp.stat.refused":
    "ردشده",
  "sp.stat.down":
    "سرویس از کار افتاده",
  "sp.stat.timeouts":
    "۱۰ ثانیه ساکت",
  "sp.stat.evicted":
    "بیرون‌رانده‌شده",
  "sp.hint.accepted":
    "اتصال‌هایی که فرانت از زمان راه‌اندازی عامل پذیرفته",
  "sp.hint.default":
    "بدون نام، با نام ناشناخته، غیر TLS یا hello خراب: به سرویس پیش‌فرض رفته‌اند",
  "sp.hint.refused":
    "بسته‌شده چون سرویس اتصال را به‌موقع نپذیرفت",
  "sp.hint.down":
    "نام متعلق به سرویسی بود که اجرا نمی‌شد: بازدیدکننده به سرویس پیش‌فرض رفت",
  "sp.hint.timeouts":
    "۱۰ ثانیه چیزی نگفتند و بسته شدند",
  "sp.hint.evicted":
    "دست‌دادن‌های ناتمامی که وقتی پورت پر شد برای باز کردن جا کنار گذاشته شدند",
  "sp.since":
    "از زمان راه‌اندازی عامل شمرده می‌شود · گزارش {when}",
  "sp.unknown":
    "نام‌هایی که بازدیدکنندگان خواستند و هیچ سرویسی ندارد",
  "sp.bind":
    "نود نتوانست پورت {port} را بگیرد: {error}. سرویس‌های روی آن اجرا نمی‌شوند؛ نود خودش دوباره تلاش می‌کند.",
  "sp.waiting":
    "نود هنوز دربارهٔ این پورت گزارش نداده است. شمارنده‌ها طی چند دقیقه ظاهر می‌شوند.",
  "sp.old.title":
    "این نود هنوز نمی‌تواند پورت را تقسیم کند",
  "sp.old.body":
    "عامل آن هنوز نمی‌داند چطور پورت TCP را تقسیم کند. روی پورت {port} فقط «{default}» اجرا می‌شود؛ {others} تا به‌روزرسانی عامل منتظر می‌مانند.",
  "sp.old.go":
    "باز کردن به‌روزرسانی‌ها",
  "node.inbound.err.shareOld":
    "عامل نود هنوز نمی‌تواند پورت TCP را تقسیم کند، برای همین این پروفایل به آن فرستاده نمی‌شود. عامل را به‌روز کن تا کنار سرویس دیگر راه بیفتد.",
  "node.inbound.err.shadowed":
    "پروفایل دیگری روی این پورت نامی را دارد که این یکی به آن پاسخ می‌دهد، برای همین اجرا نمی‌شود. برایش نام جدا بگذار.",
  "event.front_overloaded":
    "پورت {port} با دست‌دادن‌های ناتمام پر شد: فرانت برای ایستادن روی پا {evicted} تا از آن‌ها را کنار گذاشت. سرویس‌های روی پورت به کار ادامه دادند.",
  // the dialogs and the card of a shared TCP port (S5)
  "share.row.held":
    "نام {name} را «{profile}» گرفته است",
  // the dialogs and the card of a shared TCP port (S5)
  "share.dnsNext":
    "رکورد DNS لازم برای این نام در گام بعد و پیش از ذخیره نشان داده می‌شود.",
};

export const tr: typeof en = {
  "err.sni_taken":
    "{node} düğümündeki {port} portunu birkaç hizmet paylaşabilir; bunlar bağlantıdaki ada (SNI) göre ayrılır. “{name}” adı zaten “{profile}” profilinde. Bu profile o düğümde kendine ait bir ad ver (düğüm adresinin bir alt alan adı ve düğüme giden bir A kaydı) ya da başka bir port seç.",
  "err.sni_taken_free":
    "{node} düğümündeki {port} portunu birkaç hizmet paylaşabilir; bunlar bağlantıdaki ada (SNI) göre ayrılır. “{name}” adı zaten “{profile}” profilinde. Bu profile o düğümde kendine ait bir ad ver (düğüm adresinin bir alt alan adı ve düğüme giden bir A kaydı) ya da boş olan {free} portunu al.",
  "err.share_needs_site":
    "{node} düğümündeki {port} portu ancak hizmetlerden biri ad göndermeyen ya da bilinmeyen bir ad gönderen ziyaretçiye düğümün kendi sitesini gösteriyorsa paylaşılır. “{profile}” ve bu profil başka bir siteyi gösteriyor. O porta düğümün kendi sitesini gösteren bir profil koy (düğümün kendi sitesiyle VLESS REALITY, TrustTunnel, tuzak siteli MTProto) ya da başka bir port seç.",
  "err.share_unsupported": "“{profile}” ({protocol}) {node} düğümündeki {port} portunu paylaşamaz: onu ayırt edecek kendi alan adı yok. Ona bir alan adı ver ya da başka bir port seç.",
  "err.share_agent_old": "{node} düğümündeki {port} portunu “{profile}” profili kullanıyor ve portu paylaşmak için düğümde daha yeni bir ajan gerekiyor. Önce düğümün ajanını güncelle ya da başka bir port seç.",
  "node.check.shared":
    "{node} düğümündeki TCP {port} portunu {profiles} paylaşıyor. Düğüm bunları bağlantıdaki ada göre ayırır; ad göndermeyen ya da bilinmeyen bir ad gönderen ziyaretçi “{default}” sitesini görür.",
  "node.check.sharedRestart": "“{restarts}” yeniden başlar, açık bağlantıları yeniden bağlanır.",
  "node.check.twoFaces":
    "{node} düğümünün TCP {port} portunda “{profile}” başka bir siteyi gösteriyor. Onun adını isteyen ziyaretçi o siteyi, adsız ya da başka adlı ziyaretçi “{default}” sitesini görür; çok sayıda ad deneyen biri bu iki siteyi ayırt edebilir.",
  "approval.value.warn.tcp_port_shared": "TCP portu başka hizmetlerle paylaşılıyor",
  "approval.value.warn.share_two_faces": "paylaşılan porttaki bir hizmet başka bir siteyi gösteriyor",
  "approval.fact.sni": "Düğümdeki alan adı (SNI)",
  // the dialogs and the card of a shared TCP port (S5)
  "share.dns.title":
    "Ad için DNS kaydı",
  "share.dns.copy":
    "Kaydı kopyala",
  "share.dns.type":
    "Tür",
  "share.dns.name":
    "Ad",
  "share.dns.value":
    "Değer",
  "share.dns.hint":
    "Alan adının DNS’inin tutulduğu yerde düz bir kayıt olarak ekle (proxy olmadan: Cloudflare’de gri bulut). Kayıt düğüme ulaşınca düğüm, adın sertifikasını Let’s Encrypt’ten kendisi alır.",
  "share.dns.hintCname":
    "Alan adının DNS’inin tutulduğu yerde, proxy olmadan ekle (Cloudflare’de gri bulut). DNS’in burada CNAME kabul etmiyorsa düğümün IPv4 adresiyle bir A kaydı kullan. Ad düğüme ulaşınca düğüm sertifikayı Let’s Encrypt’ten kendisi alır.",
  "share.openUpdates":
    "Güncellemeleri aç",
  "share.title":
    "“{profile}” profilinin yanına {port} portunda koy",
  "share.body":
    "İkisi de {node} düğümünün {port} portunda duracak ve düğüm bunları bağlantıdaki ada (SNI) göre ayırır. “{profile}” zaten {held} adına yanıt veriyor, bu yüzden bu profilin kendi adı olmalı. Aşağıdaki, düğüm adresinin düz bir alt alan adı; istersen değiştir.",
  "share.bodyIp":
    "İkisi de {node} düğümünün {port} portunda duracak ve düğüm bunları bağlantıdaki ada (SNI) göre ayırır. “{profile}” zaten {held} adına yanıt veriyor, bu yüzden bu profilin kendi adı olmalı. {node} bir IP adresiyle biliniyor; bu yüzden denetlediğin bir alan adını yaz ve düğüme yönlendir.",
  "share.name":
    "Bu profilin {node} üzerindeki adı",
  "share.nameBad":
    "Bu bir ana makine adı değil: noktayla ayrılmış parçalarda harf, rakam ve tire kullan, IP adresi olmaz.",
  "share.put":
    "{port} portunda yanına koy",
  "share.answersTo":
    "O portta {name} adına yanıt verir.",
  "share.row.shared":
    "{port} portunda {profiles} ile yan yana durur",
  "sp.title":
    "TCP {port} paylaşılıyor",
  "sp.sub":
    "Bu portta {n} hizmet yanıt veriyor. Düğüm bunları bağlantıdaki ada göre ayırır; ad göndermeyen ya da bilinmeyen bir ad gönderen ziyaretçi “{default}” hizmetine gider.",
  "sp.default":
    "Varsayılan",
  "sp.default.hint":
    "Ad göndermeyen, bilinmeyen ad gönderen ya da TLS olmayan ziyaretçiyi alır",
  "sp.col.name":
    "Ad",
  "sp.col.got":
    "Bağlantı",
  "sp.off":
    "kapalı",
  "sp.stat.accepted":
    "Kabul edilen",
  "sp.stat.default":
    "Varsayılana giden",
  "sp.stat.refused":
    "Reddedilen",
  "sp.stat.down":
    "Hizmet çalışmıyordu",
  "sp.stat.timeouts":
    "10 sn sessiz",
  "sp.stat.evicted":
    "Düşürülen",
  "sp.hint.accepted":
    "Ajan başladığından beri ön ucun kabul ettiği bağlantılar",
  "sp.hint.default":
    "Adsız, bilinmeyen adlı, TLS olmayan ya da bozuk hello’lu ziyaretçiler: varsayılan hizmete gitti",
  "sp.hint.refused":
    "Hizmet bağlantıyı zamanında almadığı için kapatılanlar",
  "sp.hint.down":
    "Ad çalışmayan bir hizmete aitti: ziyaretçi varsayılan hizmete gitti",
  "sp.hint.timeouts":
    "10 saniye hiçbir şey söylemedi ve kapatıldı",
  "sp.hint.evicted":
    "Port doldurulduğunda yer açmak için düşürülen tamamlanmamış el sıkışmalar",
  "sp.since":
    "Ajan başladığından beri sayılır · bildirildi {when}",
  "sp.unknown":
    "Ziyaretçilerin istediği ama hiçbir hizmetin tutmadığı adlar",
  "sp.bind":
    "Düğüm {port} portunu alamadı: {error}. Üzerindeki hizmetler çalışmıyor; düğüm kendisi yeniden dener.",
  "sp.waiting":
    "Düğüm bu port için henüz rapor vermedi. Sayaçlar birkaç dakika içinde görünür.",
  "sp.old.title":
    "Bu düğüm henüz portu paylaşamıyor",
  "sp.old.body":
    "Ajanı bir TCP portunu paylaşmayı bilmiyor. {port} portunda yalnızca “{default}” çalışıyor; {others} ajan güncellenene kadar bekliyor.",
  "sp.old.go":
    "Güncellemeleri aç",
  "node.inbound.err.shareOld":
    "Düğümün ajanı henüz bir TCP portunu paylaşamıyor, bu yüzden bu profil ona gönderilmiyor. Ajanı güncelle; diğer hizmetin yanında başlar.",
  "node.inbound.err.shadowed":
    "Bu portta başka bir profil bu profilin yanıt verdiği adı tutuyor, bu yüzden çalışmıyor. Ona kendi adını ver.",
  "event.front_overloaded":
    "{port} portu tamamlanmamış el sıkışmalarla dolduruldu: ön uç ayakta kalmak için {evicted} tanesini düşürdü. Porttaki hizmetler çalışmaya devam etti.",
  // the dialogs and the card of a shared TCP port (S5)
  "share.row.held":
    "{name} adı “{profile}” profilinde",
  // the dialogs and the card of a shared TCP port (S5)
  "share.dnsNext":
    "Bu adın ihtiyaç duyduğu DNS kaydı, kaydetmeden önce bir sonraki adımda gösterilir.",
};
