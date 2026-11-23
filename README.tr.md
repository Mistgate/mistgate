<div align="center">

<img src="web/public/favicon.svg" width="96" height="96" alt="Mistgate logosu">

<h1>Mistgate</h1>

<p>Küçük bir VPN için kendi sunucunuzda çalışan bir panel: size ait birkaç sunucu ve onları paylaştığınız insanlar.<br>
Hysteria2, VLESS ve AmneziaWG. İki Go ikili dosyası, SQLite ve systemd.</p>

<p>
<img alt="Durum: beta" src="https://img.shields.io/badge/durum-beta-f2c46b">
<a href="https://github.com/Mistgate/mistgate/releases/latest"><img alt="Son sürüm" src="https://img.shields.io/github/v/release/Mistgate/mistgate?sort=semver&color=b8acf2"></a>
<a href="LICENSE"><img alt="Lisans: AGPL-3.0" src="https://img.shields.io/github/license/Mistgate/mistgate?color=b8acf2"></a>
<a href="https://github.com/Mistgate/mistgate/actions/workflows/ci.yml"><img alt="CI" src="https://github.com/Mistgate/mistgate/actions/workflows/ci.yml/badge.svg?branch=main"></a>
<a href="go.mod"><img alt="Go sürümü" src="https://img.shields.io/github/go-mod/go-version/Mistgate/mistgate"></a>
</p>

<p><a href="https://mistgate.app/"><b>Belgeler</b></a> · <a href="#hızlı-başlangıç"><b>Kurulum</b></a> · <a href="#protokoller"><b>Protokoller</b></a> · <a href="#kararlı-mı"><b>Kararlı mı?</b></a> · <a href="#diğer-panellerle-karşılaştırma"><b>Karşılaştırma</b></a> · <a href="README.md">English</a> · <a href="README.ru.md">Русский</a> · <a href="README.zh.md">简体中文</a> · <a href="README.fa.md">فارسی</a></p>

<p><sub><b>Beta.</b> 1.0'a kadar değişiklikler olabilir. <a href="https://mistgate.app/roadmap/status/">Durum ve yol haritası</a></sub></p>

</div>

<img alt="Mistgate tanıtımı: tarayıcıdan SSH ile bir düğüm eklenir, bir profille çalışmaya başlar ve hemen bir kişinin sayfasında görünür" src="docs/images/video/hero-tr.webp" width="100%">

## Neler yapar

Paneli bir sunucuya kurar, VPN sunucularınızı ona düğüm olarak eklersiniz. Her kişi tek bir abonelik bağlantısı alır ve uygulaması, kullanabileceği bütün sunucuları kendiliğinden çeker. Panel düğümlerin yapılandırmasını güncel tutar ve onları kontrol eder.

<p align="center"><img alt="How Mistgate fits together" src="docs/en/diagrams/how-it-fits.svg" width="720"></p>

Buradaki her şey uydurma demo verileriyle çalışır; klipler gerçek yönetim panelinin kısa döngüleridir.

<table>
<tr>
<td width="38%" valign="top">
<h3>Sunucuyu tarayıcıdan ekleyin</h3>
<p>Sunucunun IP adresini yazın, panelin nasıl giriş yapacağını seçin (hizmet sağlayıcınızda bir kez eklediğiniz panelin kendi SSH anahtarı ya da root parolası) ve kurulumu izleyin. Panel sunucunun SSH anahtarını sabitler, sunucuyu kontrol eder, etkin UFW veya firewalld içinde portları açar ve imzalı ajanı kurar. Yeni sunucuda terminal gerekmez; isterseniz tek seferlik bir komut da var.</p>
<p><a href="docs/tr/getting-started/add-node.md">Belgeler →</a></p>
</td>
<td width="62%"><picture><source media="(prefers-color-scheme: dark)" srcset="docs/images/readme/addnode-dark-en.webp"><source media="(prefers-color-scheme: light)" srcset="docs/images/readme/addnode-light-en.webp"><img alt="&quot;Yeni düğüm&quot; penceresi: sunucunun adresi, sonra giriş, kontroller ve kurulum adımları düğüm bağlanana kadar art arda" src="docs/images/readme/addnode-dark-en.webp" width="100%"></picture></td>
</tr>
<tr>
<td width="38%" valign="top">
<h3>Kişi başına tek bağlantı ve kendi sayfası</h3>
<p>Her kişi tek bir abonelik bağlantısı, bir QR kodu ve arkasında bir sayfa alır: kendi platformu için yönergeler, kalan günler, trafik ve kendi cihazları. Mihomo tabanlı uygulamalar bağlantıdan bütün protokolleri alır, Happ Hysteria2 ve VLESS'i alır, AmneziaVPN ise sayfadan her cihaz için bir anahtar alır. Gruplar, her kişinin hangi sunuculara ulaşacağını belirler.</p>
<p><a href="docs/tr/guide/user-page.md">Belgeler →</a></p>
</td>
<td width="62%"><picture><source media="(prefers-color-scheme: dark)" srcset="docs/images/readme/link-dark-en.webp"><source media="(prefers-color-scheme: light)" srcset="docs/images/readme/link-light-en.webp"><img alt="Yönetim panelinde bir kişinin kartı: trafik, abonelik bağlantısı ve QR kodunun penceresi" src="docs/images/readme/link-dark-en.webp" width="100%"></picture></td>
</tr>
<tr>
<td width="38%" valign="top">
<h3>İstemcinin gözünden kontroller</h3>
<p>Panel her 5 dakikada bir, bir uygulama gibi her düğümdeki her protokole bağlanır ve her tünelden geçen gecikmeyi gösterir; bozuk bir sunucuyu onu kullananlardan önce siz öğrenirsiniz. Her düğümdeki Teşhis 23 sunucu kontrolü yapar ve on düzeltme önerir; hiçbiri onayınız olmadan çalışmaz.</p>
<p><a href="docs/en/operations/health.md">Belgeler →</a></p>
</td>
<td width="62%"><picture><source media="(prefers-color-scheme: dark)" srcset="docs/images/readme/health-dark-en.webp"><source media="(prefers-color-scheme: light)" srcset="docs/images/readme/health-light-en.webp"><img alt="İstemci gözüyle kontrol tablosu: düğümler ve protokoller, her tünelden geçen gecikme, nedenini göstermek için açılmış bozuk bir düğüm" src="docs/images/readme/health-dark-en.webp" width="100%"></picture></td>
</tr>
<tr>
<td width="38%" valign="top">
<h3>Birkaç protokol için tek TCP 443</h3>
<p>Birçok ağ yalnızca TCP 443'ü geçirir. Bir düğüm VLESS REALITY, TLS üzerinden VLESS, TrustTunnel ve MTProto'yu bu tek portta tutabilir; ajan onları istemcinin istediği ada göre ayırır. Dolu bir porta ikinci bir profil koyduğunuzda panel onu birincinin yanına koymayı önerir ve eklemeniz gereken DNS kaydını gösterir.</p>
<p><a href="docs/en/guide/shared-port.md">Belgeler →</a></p>
</td>
<td width="62%"><picture><source media="(prefers-color-scheme: dark)" srcset="docs/images/readme/shared-dark-en.webp"><source media="(prefers-color-scheme: light)" srcset="docs/images/readme/shared-light-en.webp"><img alt="Bir profili 443 portunda başka birinin yanına koyma önerisi, sonra sayaçlarıyla ortak port kartı" src="docs/images/readme/shared-dark-en.webp" width="100%"></picture></td>
</tr>
<tr>
<td width="38%" valign="top">
<h3>Şablondan kamuflaj siteleri</h3>
<p>Düğümü açan yabancı, Mistgate sayfası değil, sıradan küçük bir site görmeli. Bir ülke ve on türden birini seçin (durum sayfası, belgeler, blog, küçük işletme, portfolyo ve diğerleri); panel o ülkenin dilinde, kendi görünümü ve metinleriyle bir site kursun, her seferinde farklı. Bakın, başka bir sürüm isteyin, sonra kaydedin. Kendi ücretsiz fotoğraf anahtarınızla siteye gerçek fotoğraflar da gelir; onları düğümün kendisi sunar.</p>
<p><a href="docs/en/guide/decoy-site.md#a-site-from-a-template">Belgeler →</a></p>
</td>
<td width="62%"><picture><source media="(prefers-color-scheme: dark)" srcset="docs/images/readme/decoys-dark-en.webp"><source media="(prefers-color-scheme: light)" srcset="docs/images/readme/decoys-light-en.webp"><img alt="&quot;Şablondan&quot; penceresi: bir ülke, bir tür, yeni sitenin önizlemesi ve &quot;Başka sürüm&quot; düğmesi" src="docs/images/readme/decoys-dark-en.webp" width="100%"></picture><br><picture><source media="(prefers-color-scheme: dark)" srcset="docs/images/readme/decoys-sites-dark-en.webp"><source media="(prefers-color-scheme: light)" srcset="docs/images/readme/decoys-sites-light-en.webp"><img alt="Üreticinin yaptığı beş türden beş site yan yana kayıyor" src="docs/images/readme/decoys-sites-dark-en.webp" width="100%"></picture></td>
</tr>
<tr>
<td width="38%" valign="top">
<h3>Kademeli ve geri alınabilir güncellemeler</h3>
<p>Düğümler kendiliğinden güncellenmez. Kademeli güncelleme önce tek bir kanarya düğümünü günceller ve ona sağlık kontrolünü geçmesi için 5 dakika verir, sonra küçük gruplarla sürer. Kontrolü geçemeyen düğüm önceki sürümünü geri alır ve güncelleme durur. Panel yalnızca siz onaylayınca kendini günceller ve yeni sürüm ayakta kalamazsa eski programı ve verileri geri koyar.</p>
<p><a href="docs/en/operations/updates.md">Belgeler →</a></p>
</td>
<td width="62%"><picture><source media="(prefers-color-scheme: dark)" srcset="docs/images/readme/updates-dark-en.webp"><source media="(prefers-color-scheme: light)" srcset="docs/images/readme/updates-light-en.webp"><img alt="Güncellemeler sayfası: kademeli güncelleme kanaryayı kurar, 5 dakika kontrol eder, sonra diğer düğümlere geçer" src="docs/images/readme/updates-dark-en.webp" width="100%"></picture></td>
</tr>
<tr>
<td width="38%" valign="top">
<h3>Sunucuların işletim sistemi güncellemeleri</h3>
<p>Hangi sunucuların güvenlik güncellemesi ya da yeniden başlatma beklediğini, her birine girmeden görün. Bir sunucuya güvenlik güncellemesi kurabilir ya da onu yeniden başlatabilirsiniz; birkaç sunucuyu işaretleyip kademeli olarak da çalıştırabilirsiniz: her seferinde birkaç sunucu, her birinden sonra sağlık kontrolü, ilk başarısızlıkta durma.</p>
<p><a href="docs/en/operations/updates.md#server-operating-system">Belgeler →</a></p>
</td>
<td width="62%"><picture><source media="(prefers-color-scheme: dark)" srcset="docs/images/readme/os-dark-en.webp"><source media="(prefers-color-scheme: light)" srcset="docs/images/readme/os-light-en.webp"><img alt="Sunucu sistemleri sekmesi: çekirdekleri ve bekleyen güncellemeleriyle sunucular, ikisi işaretli, kuruluyor, bitti" src="docs/images/readme/os-dark-en.webp" width="100%"></picture></td>
</tr>
<tr>
<td width="38%" valign="top">
<h3>WARP çıkışı ve çalıştığının kontrolü</h3>
<p>Bir profilin trafiğini Cloudflare WARP üzerinden çıkarın, düğüm başına tek hesapla. Kart tüneli, trafiği ve çıkış kontrolünü gösterir; panel ayrıca her düğümün WARP'ı üzerinden bir site listesi çeker ve bazıları onu engellemeye başlarsa uyarı verir. WARP çıkışlı bir profil hiçbir zaman düğümün kendi adresine dönmez.</p>
<p><a href="docs/en/guide/warp.md">Belgeler →</a></p>
</td>
<td width="62%"><picture><source media="(prefers-color-scheme: dark)" srcset="docs/images/readme/warp-dark-en.webp"><source media="(prefers-color-scheme: light)" srcset="docs/images/readme/warp-light-en.webp"><img alt="Bir düğümün WARP kartı: tünel açık ve çıkış kontrolü geçiyor, sonra tünel düşüyor ve kart yeniden başlatmayı öneriyor" src="docs/images/readme/warp-dark-en.webp" width="100%"></picture></td>
</tr>
<tr>
<td width="38%" valign="top">
<h3>Uygulamalar için DNS ön ayarları</h3>
<p>Ön ayar, bir kişinin uygulamasının hangi DNS sunucularına ve nasıl soracağını belirler (düz, DoT veya DoH); bir ülke ya da site listesi için ayrı kurallar ve uygulamalarda reklam engelleme de olabilir. Herkese, bir gruba ya da tek bir kişiye verin; düzenleyici, her uygulamanın gerçekte ne alacağını gösterir.</p>
<p><a href="docs/en/guide/dns.md">Belgeler →</a></p>
</td>
<td width="62%"><picture><source media="(prefers-color-scheme: dark)" srcset="docs/images/readme/dns-dark-en.webp"><source media="(prefers-color-scheme: light)" srcset="docs/images/readme/dns-light-en.webp"><img alt="Abonelikler'in DNS sekmesi: ön ayarlar, sonra biri sunucuları ve her uygulamanın alacağıyla açık, sonra ayrım kuralları" src="docs/images/readme/dns-dark-en.webp" width="100%"></picture></td>
</tr>
<tr>
<td width="38%" valign="top">
<h3>Asistan ve MCP: önce plan</h3>
<p>Yerleşik asistan (kendi anahtarınızla bağladığınız bir bulut modeli) düğümleriniz ve kişiler hakkında yanıt verir, değişiklikleri hazırlar. MCP aynısını dışarıdaki bir yapay zekâ ajanına verir: filoyu okur, değişiklik planlar; riskli her değişikliği uygulanmadan önce siz yönetim panelinde onaylarsınız. Bir API belirteci, siz sınırı kaldırmadıkça saatte en fazla 10 kişiyi devre dışı bırakabilir veya yeniden yazabilir.</p>
<p><a href="docs/en/guide/assistant.md">Belgeler →</a></p>
</td>
<td width="62%"><picture><source media="(prefers-color-scheme: dark)" srcset="docs/images/readme/assistant-dark-en.webp"><source media="(prefers-color-scheme: light)" srcset="docs/images/readme/assistant-light-en.webp"><img alt="Asistan penceresi: bir soru, önerilen değişikliklerle yanıt, &quot;Onayla ve uygula&quot; ve uygulandığını söyleyen satır" src="docs/images/readme/assistant-dark-en.webp" width="100%"></picture></td>
</tr>
<tr>
<td width="38%" valign="top">
<h3>Telegram'da uyarılar</h3>
<p>Kendi botunuzu bağlayın; bir düğüm düştüğünde ya da geri geldiğinde, bir kontrol başarısız olduğunda, yedekleme başarısız olduğunda ya da bir ajanın planı sizi beklediğinde panel sohbetinize yazsın. Yalnızca uyarı gönderir: sohbetten bir şey onaylanmaz ya da değiştirilmez. Her yönetici kendi sohbetini bağlar.</p>
<p><a href="docs/en/operations/telegram-alerts.md">Belgeler →</a></p>
</td>
<td width="62%"><picture><source media="(prefers-color-scheme: dark)" srcset="docs/images/readme/telegram-dark-en.webp"><source media="(prefers-color-scheme: light)" srcset="docs/images/readme/telegram-light-en.webp"><img alt="Entegrasyonlar sayfası: bot, Start bekleyen sohbet, sonra bağlandı ve bir deneme iletisi gönderildi" src="docs/images/readme/telegram-dark-en.webp" width="100%"></picture></td>
</tr>
<tr>
<td width="38%" valign="top">
<h3>Şifreli yedekler ve dikkatli geri yükleme</h3>
<p>Yedekler kendi Cloudflare R2 kovanıza gider, zamanlanmış ya da isteğe bağlı olarak, panelden çıkmadan önce çevrimdışı sakladığınız anahtarla şifrelenir. Geri yükleme önce yedeği kontrol eder ve içindekini şimdi sahip olduklarınızla karşılaştırır; siz onaylamadan hiçbir şey değişmez ve geri yüklemeden önceki veriler saklanır.</p>
<p><a href="docs/en/operations/backups.md">Belgeler →</a></p>
</td>
<td width="62%"><picture><source media="(prefers-color-scheme: dark)" srcset="docs/images/readme/backups-dark-en.webp"><source media="(prefers-color-scheme: light)" srcset="docs/images/readme/backups-light-en.webp"><img alt="&quot;Yedekten geri yükle&quot; penceresi: dosya kontrol edildi, özet şimdiki durumla karşılaştırılıyor, geri yükleme, panel geri geldi" src="docs/images/readme/backups-dark-en.webp" width="100%"></picture></td>
</tr>
</table>

Ayrıca:

- Bazı ağlardan bir sunucuya ulaşılamıyorsa kişiler ona başka bir düğümünüz üzerinden bağlanır; o düğüm yalnızca şifreli trafiği iletir. Panel yolu açmadan önce kontrol eder, sonra da izlemeye devam eder; düğüm sayfasındaki Giriş yolları, kişilerin sunucuya nasıl ulaştığını gösteren bir grafiktir; Düğümler sayfasında ise tüm düğümlerin haritası vardır. [Aktarıcılar](docs/en/guide/relays.md)
- İsteğe bağlı olarak, sunucularınızın bir ülkenin içinden açılıp açılmadığını ağ ağ kontrol edebilirsiniz: ev ve mobil ağlarda IPv4 ve IPv6 üzerinden Globalping ölçümleri, gönüllü ve SIM ölçüm noktaları, engelleme listeleri ve kendi düğümünüzden bir TSPU ölçümü. Kaynakların çoğu yalnızca Rusya'da çalışır. [Erişilebilirlik](docs/en/operations/reachability.md)
- Docker yok, veritabanı sunucusu yok. Panel boştayken 1 vCPU / 1 GB'lık bir sunucuda 16-20 MiB bellek kullanır. [Performans testleri](docs/en/reference/benchmarks.md), Mistgate'in daha yavaş kaldığı yerler dahil.
- Panelin adresini açan yabancılar bir kamuflaj sitesi görür; yönetim paneli gizli bir yolun ya da ana bilgisayar adının arkasındadır.
- Hangi sunucu kime: bir düğüm herkese, seçili gruplara ya da şimdilik hiç kimseye verilir; bir kişi birkaç grupta olabilir. [Gruplar](docs/en/guide/users-and-groups.md)
- Windows için bir masaüstü uygulaması, [Mistgate](https://github.com/Mistgate/mistgate-client), bu panellerle ve düz abonelik bağlantılarıyla çalışır.

Henüz olmayanlar: uyarılar dışında bir Telegram botu; Cloudflare Workers sürümü de hâlâ bir [önizleme](docs/en/getting-started/install-edge.md). Bazı varsayılanlar Rusya'daki kullanıcılara göre ayarlıdır; hepsi ayardır.

## Protokoller

| Protokol | Düğümde | Neler ayarlanabilir |
|:--|:--|:--|
| [Hysteria2](docs/en/guide/hysteria2.md) | Resmî Hysteria2 çekirdeği | UDP (QUIC). Salamander gizleme (Gecko deneysel), port atlama, Let's Encrypt sertifikası ya da parmak izi sabitlenmiş kendinden imzalı sertifika. |
| [VLESS](docs/en/guide/vless.md) | xray-core | XHTTP ya da TCP (RAW + Vision) üzerinden REALITY; XHTTP ya da WebSocket üzerinden TLS, doğrudan veya bir CDN arkasında. |
| [AmneziaWG](docs/en/guide/amneziawg.md) | Kullanıcı alanında amneziawg-go ya da çekirdek modülü | 3.1 (varsayılan) ve 2.0 sürümleri, her cihaz için ayrı anahtar. 1.5 ve 1.0 sunulmaz. |
| [TrustTunnel](docs/en/guide/trusttunnel.md) | Düğüm ajanının içindeki Mistgate'in kendi motoru | Deneysel. Sıradan HTTPS içinde bir VPN (HTTP/2, isteğe bağlı HTTP/3), gerçek sertifikayla; resmî TrustTunnel uygulamaları için `tt://` anahtarları. |
| [MTProto](docs/en/guide/mtproto.md) | Mistgate'in kendi MTProto çekirdeği | Deneysel. Yalnızca Telegram uygulaması için bir proxy: ad olarak düğümün kendi alan adını ya da başka bir siteyi kullanan Fake TLS, kişi başına bir anahtar, geri kalan herkese kamuflaj sitesi. |

Hysteria2 ve VLESS, TCP 443 dahil, aynı düğümü paylaşabilir. Her uygulama biçimi her profili taşımaz: [ne nerede çalışır](docs/en/guide/subscriptions.md#what-works-where).

Henüz desteklenmeyenler: VMess, Trojan, Shadowsocks, TUIC ve düz WireGuard. Başka protokoller de planlanıyor; her yenisi aşağıdaki sözleşme aracılığıyla aynı abonelik bağlantısına eklenir.

Bir protokol, ikili dosyalara derlenmiş iki yarıdan oluşur. Panel tarafında tek bir Go arayüzü: ayarları için bir JSON Schema (yönetim paneli formu bundan oluşturur), doğrulama, düğümün çalıştıracağı tanım, kimlik bilgilerinin verilmesi ve her abonelik biçimindeki kendi parçası. Düğüm tarafında ise bu tanımı çalıştıran, kimlik bilgilerini yeniden başlatmadan değiştiren, trafiği ve sağlık durumunu bildiren bir motor. Yeni bir protokol yalnızca onu desteklediğini bildiren ajanlara gönderilir; böylece eski ajanlar çalışmaya devam eder. Ayrıntılar: [protokol eklentisi sözleşmesi](docs/en/reference/architecture.md#the-protocol-plugin-contract).

## Kararlı mı?

Beta aşamasında ve henüz genç: v0.1.0 etiketi 2026-10-03'te atıldı, 0.2.0 hattı ise bütün arayüzü baştan çizdi. 1.0'a kadar API, kayıtlı ayarlar ve düğüm protokolü değişebilir. Sürümler henüz eski sürümlerden otomatik bir yükseltme testinden geçmiyor; bu, [1.0 koşullarından](docs/en/roadmap/status.md) biri.

Ben kendi altı sunucumda, ailem ve arkadaşlarım için kullanıyorum.

Tüneller panele bağlı değildir: panel kapalıyken düğüm son uygulanan yapılandırmayla hizmet vermeyi sürdürür, bir kullanıcı değişikliği sunucuyu yeniden başlatmaz ve eski ajanlar daha yeni bir panelle çalışır. [Performans testinde](docs/en/reference/benchmarks.md) kullanıcı ekleyip çıkarmak ve paneli yeniden başlatmak hiçbir bağlantıyı koparmadı.

Kontroller:

- Her push ve pull request'te CI ([ci.yml](.github/workflows/ci.yml)): Go testleri ve vet, panel-ajan oturumunda yarış dedektörü, `govulncheck`, yönetim panelinin lint ve testleri, belgelerin derlenmesi, Debian ve Ubuntu'da kurulum betiği.
- Uçtan uca bir çalıştırma ([scripts/e2e-wsl.sh](scripts/e2e-wsl.sh)): gerçek Hysteria2, AmneziaWG, Xray, mihomo ve sing-box istemcileriyle gerçek bir panel ve düğüm; ayrıca ajan güncellemeleri, geri almalar ve SSH üzerinden kurulum. Her sürümden önce WSL'de ya da bir sanal makinede elle çalıştırılır, CI'da değil.
- GitHub Actions her etiketi taslak olarak derler. Ben onu çevrimdışı sürüm anahtarının bulunduğu bir makinede etiketten yeniden derler ve yalnızca bayt bayt eşleşen ikili dosyaları imzalarım; düğümler ve panel yalnızca imzalı derlemeleri kurar. [Sürümler ve imzalama](docs/en/operations/releases.md)

<p align="center"><img alt="What an update is trusted for" src="docs/en/diagrams/update-trust.svg" width="720"></p>

Güncelleme ([Güncellemeler](docs/en/operations/updates.md)):

- Düğümler asla kendiliğinden güncellenmez. Bir dağıtım önce tek bir kanarya düğümünü günceller ve sağlık kontrolünü geçmesi için ona 5 dakika tanır, sonra birer ya da ikişer düğümlük gruplarla devam eder. Başarısız olan düğüm önceki derlemesine döner ve dağıtım duraklar.
- Yeni derleme 5 dakika içinde bağlanamazsa ya da sürekli çökerse ajan kendini de geri alır. Her düğüm elle de geri alınabilir.
- Panelle bağlantısı takılan bir düğüm, güncellemenin indirmesini GitHub'dan tamamlayabilir. Panel onu ikinci kaynak olarak yalnızca oradaki sürüm panelin kendi anahtarıyla imzalıysa ve aynı dosyaları içeriyorsa sunar; varsayılan olarak açıktır, ayarlardan kapatılır ve GitHub düğümün adresini görür.
- Panel kendini yalnızca siz onayladığınızda günceller. Önce verilerini yedekler; yeni sürüm 45 saniye ayakta kalamazsa eski ikili dosyayı ve verileri geri yükler.
- Yine de `/var/lib/mistgate` dizininin [şifreli yedeklerini](docs/en/operations/backups.md) alın.

Mistgate yapay zekâ kodlama ajanlarıyla yazılıyor. Yazdıklarını ben gözden geçiriyor, neyin birleştirilip yayımlanacağına ben karar veriyorum; yukarıdaki kontroller onların kodu için de herkesin kodu için olduğu gibi aynıdır. Bir ajanla yapılan commit'ler bir `Co-Authored-By` satırı taşır; ajanların bu depoda uyduğu kurallar [AGENTS.md](AGENTS.md) dosyasındadır.

## Hızlı başlangıç

Gerekenler: systemd'li, temiz kurulmuş bir Linux sunucusu (amd64 ya da arm64), doğrudan ona yönlendirilmiş bir alan adı (burada `panel.example.com`), boş TCP 80 ve 443 portları ve `ssh-keygen -Y verify` destekli `openssh-client` ya da OpenSSL 3.2 veya üstü. Sunucuda şunu çalıştırın:

```sh
curl -fsSL https://mistgate.app/install.sh | sudo bash -s -- --public-url https://panel.example.com
```

Kurulum betiği imzalı panel manifestini doğrular, Mistgate'i systemd altında başlatır ve yönetim panelinin adresini tek kullanımlık bir kurulum bağlantısıyla (30 dakika geçerli) yazdırır. Güvenlik duvarına dokunmaz. `--yes` soruları atlar, `--dry-run` yalnızca sürümü doğrular; elle kurulum ve diğer seçenekler [Paneli kurma](docs/en/getting-started/install-panel.md) sayfasında.

Ardından:

1. Kurulum bağlantısını açın ve sahibi oluşturun: bir geçiş anahtarıyla ya da parola ve doğrulayıcı uygulama koduyla.
2. Sunucuları **Düğümler → Düğüm ekle** bölümünden, SSH üzerinden ya da tek kullanımlık bir komutla ekleyin ([Düğüm ekleme](docs/en/getting-started/add-node.md)).
3. Kişileri ekleyin: [İlk kullanıcılar](docs/en/getting-started/first-users.md).

`ssh` çalıştırabilen bir yapay zekâ ajanı bu adımları [yapay zekâ ajanı kılavuzundaki](docs/en/getting-started/ai-agents.md#install-mistgate-with-an-ai-agent) istemle yapabilir; sahip hesabını yine de kendiniz oluşturursunuz.

## Diğer panellerle karşılaştırma

| | Mistgate | 3x-ui | Remnawave | Hiddify | PasarGuard | s-ui |
|:--|:--|:--|:--|:--|:--|:--|
| Docker olmadan çalışır | ✅ | ✅ | ❌ | ✅ | 🟡 kısmen | ✅ |
| Veritabanı | SQLite | SQLite veya PostgreSQL | PostgreSQL + Redis | MySQL + Redis | SQLite, MySQL, MariaDB veya PostgreSQL | SQLite |
| Boştaki panelin RAM kullanımı | 16-20 MiB¹ | 68-74 MiB¹ | 426-454 MiB¹ | ölçülmedi | 262-265 MiB¹ | ölçülmedi |
| Panelden düğüm ekleme (SSH) | ✅ | ❌ | ❌ | ❌ | ❌ | ? |
| Hysteria2 | ✅ | ✅ | 🟡 kısmen | ✅ | ✅ | ✅ |
| VLESS REALITY | ✅ | ✅ | ✅ | ✅ | ✅ | ? |
| AmneziaWG | ✅ 2.0 ve 3.1 | ✅ 3.1 | ? | ? | ? | ? |
| VMess, Trojan, Shadowsocks | ❌ | ✅ | 🟡 kısmen | ✅ | ✅ | ✅ |
| Xray JSON ya da sing-box abonelikleri | ✅ ikisi de | Xray JSON | ✅ ikisi de | ✅ ikisi de | sing-box | sing-box |
| Sağlık kontrolleri ve uyarılar | ✅ istemci gözüyle kontroller, düğüm Teşhisi, bir ülkenin içinden erişilebilirlik | ✅ tünel izleme (isteğe bağlı) | ✅ durum, Prometheus | 🟡 kısmen | 🟡 kısmen | ? |
| Telegram botu | 🟡 yalnızca uyarılar | ✅ | 🟡 kısmen | ✅ | ✅ | ? |
| Yapay zekâ ajanları için yerleşik MCP sunucusu | ✅ | ? | ❌ | ? | ? | ? |

¹ Tüm panel süreç grubunun özel belleği; boşta, 0 ile 200 arası kullanıcı, bağlı düğüm yok, 1 vCPU / 1 GB'lık bir test sunucusunda (3x-ui'de Xray çekirdeği ve fail2ban dahil). Yöntem, sürümler ve sınırlar: [Performans testleri](docs/en/reference/benchmarks.md). “?”, projenin kendi README'sinin ve belgelerinin bu konuda bir şey söylemediği anlamına gelir.

Protokol çeşitliliğinde, topluluk büyüklüğünde ve tam Telegram botlarında diğerleri önde. Kaynaklarıyla birlikte, 2026-10-09'da kontrol edilmiş tam tablo [Diğer panellerle karşılaştırma](docs/en/reference/comparison.md) sayfasında.

## Belgeler

Her şey [mistgate.app](https://mistgate.app/) adresinde ve [`docs/`](docs/README.md) klasöründe. Belgeler İngilizce, Rusça ve Çince yazılmıştır; aşağıdaki bağlantılar İngilizce sürüme gider. Başlamak için iyi yerler:

- [Genel bakış](docs/en/getting-started/overview.md) ve [Gereksinimler](docs/en/getting-started/requirements.md)
- [Paneli kurma](docs/en/getting-started/install-panel.md), [SSH ile düğüm kurma](docs/en/getting-started/ssh-install.md), [İlk kullanıcılar](docs/en/getting-started/first-users.md)
- [Sağlık](docs/en/operations/health.md), [Güncellemeler](docs/en/operations/updates.md), [Şifreli yedekler](docs/en/operations/backups.md), [Sorun giderme](docs/en/operations/troubleshooting.md)
- [CLI](docs/en/reference/cli.md), [API](docs/en/reference/api.md), [MCP](docs/en/reference/mcp.md), [Mimari](docs/en/reference/architecture.md)

## Katkıda bulunmak

Hata bildirimleri, düzeltmeler ve belge katkıları memnuniyetle karşılanır. [CONTRIBUTING.md](CONTRIBUTING.md) (İngilizce) derlemeyi, kod haritasını, CI'ın çalıştırdığı kontrolleri ve kuralları anlatır. Kodlama ajanları [`AGENTS.md`](AGENTS.md) ile başlar; [yapay zekâ ajanı kılavuzunda](docs/en/getting-started/ai-agents.md#contribute-with-a-coding-agent) katkı için hazır bir istem var.

## Güvenlik

Güvenlik açıklarını gizli olarak bildirin: bkz. [SECURITY.md](SECURITY.md). Panelin veri dizinini (`/var/lib/mistgate`) yedekleyin: veritabanı, ana anahtar ve düğümlerinizin güvendiği CA orada durur.

## Lisans

Mistgate, [GNU Affero General Public License v3.0 only](LICENSE) lisansıyla dağıtılan özgür yazılımdır. Değiştirilmiş bir paneli başkaları için çalıştırıyorsanız, yönetim paneli bir “Kaynak kod” bağlantısı (`serve --source-url`) gösterir; bu bağlantı sizin kaynak kodunuza gitmelidir.

## Yıldız geçmişi

<a href="https://www.star-history.com/?repos=mistgate%2Fmistgate&type=date&legend=top-left">
 <picture>
   <source media="(prefers-color-scheme: dark)" srcset="https://api.star-history.com/chart?repos=mistgate/mistgate&type=date&theme=dark&legend=top-left" />
   <source media="(prefers-color-scheme: light)" srcset="https://api.star-history.com/chart?repos=mistgate/mistgate&type=date&legend=top-left" />
   <img alt="Yıldız geçmişi grafiği" src="https://api.star-history.com/chart?repos=mistgate/mistgate&type=date&legend=top-left" />
 </picture>
</a>
