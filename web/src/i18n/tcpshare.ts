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
};
