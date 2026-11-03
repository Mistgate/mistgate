---
title: Mistgate belgeleri
description: Belgelerde neler olduğu, nerede bulunacağı ve hangi sırayla okunacağı.
---

Mistgate, kendi VPN düğümleriniz için kendi sunucunuzda çalışan bir paneldir: panel için bir ikili dosya, düğüm ajanı için bir ikili dosya, tek abonelikte Hysteria2, VLESS REALITY ve AmneziaWG. Bu belgeler kodun bugün ne yaptığını anlatır; henüz geliştirilmekte olan özellikler **Planlanıyor** diye işaretlenir.

## Neler nerede

| Bölüm | Ne için |
|:--|:--|
| Başlarken | Kavramlar, nelere ihtiyacınız olduğu ve boş bir sunucudan çalışan aboneliği olan ilk kullanıcıya giden yol. |
| Kılavuz | Yönetim panelinde günlük iş: düğümler, profiller, protokoller, WARP, kullanıcılar, abonelikler, istemci uygulamaları, kullanıcı sayfası, DNS. |
| İşletim | Düğümleri sağlıklı, güncel ve güvenli tutmak ve bir şey bozulduğunda ne yapılacağı. |
| Yol haritası | Nelerin bittiği, nelerin denendiği, sırada ne olduğu ve bilinen sınırlar. |
| Başvuru | Tam komutlar, bayraklar, ortam değişkenleri, API, MCP sunucusu ve parçaların nasıl birbirine bağlandığı. |

## Yeni yönetici için okuma sırası

1. [Genel bakış](getting-started/overview.md) ve [Gereksinimler](getting-started/requirements.md): panel, düğüm ve profilin ne olduğu ve hangi sunuculara ihtiyacınız olduğu.
2. [Paneli kurma](getting-started/install-panel.md): resmî sürümden, elle ya da [yapay zekâ ajanı kılavuzundaki](getting-started/ai-agents.md) kurulum istemiyle. Ya da önizleme olarak, kendi sunucunuz olmadan [Cloudflare Workers üzerinde](getting-started/install-edge.md).
3. [SSH ile düğüm kurma](getting-started/ssh-install.md) ya da kendi çalıştırdığınız bir komutla [düğüm ekleme](getting-started/add-node.md).
4. [İlk kullanıcılar](getting-started/first-users.md): düğümde bir profil, bir grup, bir kullanıcı, bir abonelik bağlantısı.
5. Bağlantıları başkalarına vermeden önce [Sağlık](operations/health.md), [Güvenlik](operations/security.md) ve [şifreli yedekler](operations/backups.md).
6. Kılavuz sayfaları ihtiyaç duydukça; betik yazarken ya da otomasyon kurarken Başvuru.

## Tüm sayfalar

### Başlarken

- [Genel bakış](getting-started/overview.md): Mistgate'in ne olduğu ve kavramları: panel, düğüm, profil, düğümdeki sunucu, kullanıcı, grup, cihaz, abonelik, kullanıcı sayfası.
- [Gereksinimler](getting-started/requirements.md): panelin ve düğümlerin neye ihtiyaç duyduğu ve kaynaktan derlemek için gerekenler.
- [Paneli kurma](getting-started/install-panel.md): temiz bir Linux sunucusundan yönetim panelindeki sahip hesabına kadar.
- [Cloudflare sürümünü kurma](getting-started/install-edge.md): sunucu yerine Cloudflare Workers üzerinde panel, `mistgate edge deploy` ile (önizleme).
- [Düğüm ekleme](getting-started/add-node.md): düğüm eklemenin iki yolu, elle kurulum komutu ve bir düğümü kaldırma.
- [SSH ile düğüm kurma](getting-started/ssh-install.md): ajanı panelin kendisi kurar; kurulum işleri ve kayıtlı SSH erişimi.
- [Yapay zekâ ajanı kılavuzu](getting-started/ai-agents.md): paneli kurmak, düğümleri MCP üzerinden yönetmek ve koda katkıda bulunmak için hazır istemler.
- [İlk kullanıcılar](getting-started/first-users.md): bir düğüme profil koyma, onu bir gruba verme, kullanıcı oluşturma ve bağlantıyı gönderme.

### Kılavuz

- [Düğümler](guide/nodes.md): düğüm sayfası, durumu ve ajanın sunucuda ne yaptığı.
- [Profiller](guide/profiles.md): profilin ne olduğu ve bir düğümde nasıl sunucuya dönüştüğü.
- [Hysteria2](guide/hysteria2.md): Hysteria2 profil ayarları, sertifikalar ve gizleme.
- [VLESS REALITY](guide/vless.md): TCP ve XHTTP üzerinden VLESS profilleri, Hysteria2 yerine ne zaman seçileceği, REALITY hedefinin nasıl seçileceği, portlar ve uygulamalar.
- [Aynı düğümde VLESS ve Hysteria2](guide/vless-and-hysteria2.md): kimin nerede dinlediği, VLESS TCP 443'ü alınca Hysteria2 sitesine ve Let's Encrypt'e ne olduğu ve VLESS'i birkaç kişiyle nasıl deneyeceğiniz.
- [MTProto (Telegram proxy'si)](guide/mtproto.md): yalnızca Telegram uygulaması için bir proxy (deneysel): el sıkışmadaki adın iki seçeneği, kişinin anahtarı ve bağlantısı, aktarıcılar ve sınırları.
- [AmneziaWG](guide/amneziawg.md): AmneziaWG 2.0 ve 3.1 profilleri, kullanıcı alanı ya da çekirdek modülü, cihazlar ve anahtarlar.
- [WARP](guide/warp.md): bir profilin trafiğini Cloudflare WARP üzerinden çıkarma.
- [Giriş düğümü üzerinden aktarıcılar](guide/relays.md): ağı bir düğümü engelleyen kişilere aktarıcının ne zaman yardım ettiği, neyi çözemediği, girişin neyi gördüğü ve nasıl ekleneceği.
- [Kullanıcılar ve gruplar](guide/users-and-groups.md): kullanıcılar, gruplar, cihazlar, trafik sınırları ve süreler.
- [Abonelikler](guide/subscriptions.md): kişi başına bir bağlantı ve her uygulamanın hangi biçimi aldığı.
- [İstemci uygulamaları](guide/client-apps.md): hangi platformda hangi uygulama, her birinin ne aldığı, tek dokunuşla ekleme bağlantıları, kl!ck ve uygulamaların yönetim panelinde ve MCP üzerinden ayarlanması.
- [Kullanıcı sayfası](guide/user-page.md): kişinin kendi sayfası: talimatlar, QR kodu ve trafik.
- [DNS](guide/dns.md): kullanıcılar ve gruplar için DNS hazır ayarları ve düğümlerin DNS'i.
- [Torrent koruması](guide/torrent-protection.md): düğüm başına BitTorrent engelleyicisi, neyi yakaladığı ve olaylarının neyi gösterdiği.
- [Kendi kamuflaj siteniz](guide/decoy-site.md): yerleşik kamuflaj sayfasını kendi küçük sitenizle değiştirme ve benzersiz bir site uyduran bir yapay zekâ ajanı için hazır istem.

### İşletim

- [Sağlık](operations/health.md): istemci gözüyle kontroller, düğüm Teşhisi ve uyarılar.
- [Erişilebilirlik](operations/reachability.md): düğümlerin bir ülkenin içinden açılıp açılmadığı: Globalping, engelleme listeleri, TSPU ölçümü, her düğümün haftası ve uyarı.
- [Telegram uyarıları](operations/telegram-alerts.md): bot, sohbetinizi bağlama, ne kime gönderilir ve panel gözcüsü.
- [Panel gözcüsü](operations/panel-watcher.md): Cloudflare paneli dakikada bir kontrol eder ve kesintileri Telegram'da bildirir.
- [Güncellemeler](operations/updates.md): düğümün kendi kendini güncellemesi, bir düğümü hemen ya da zamanlanmış güncelleme, geri almalar ve paneli güncelleme.
- [Sürümler ve imzalama](operations/releases.md): sürüm anahtarı, tekrarlanabilir derlemeler, imzalama ve yayımlama, kendi paketiniz.
- [Şifreli yedekler](operations/backups.md): R2'yi yapılandırma, çevrimdışı kurtarma kimliğini koruma ve paneli geri yükleme.
- [Güvenlik](operations/security.md): yönetici girişi, roller, yeniden doğrulama, oturumlar, denetim, kamuflaj sitesi ve gizli yönetim paneli, veri dizini ve yedekler, erişimi kurtarma.
- [Sorun giderme](operations/troubleshooting.md): sık karşılaşılan sorunlar ve nedenlerinin nasıl bulunacağı.

### Yol haritası

- [Durum ve yol haritası](roadmap/status.md): mevcut sürümün neler yaptığı, sırada neler olduğu ve bilinen sınırlar.

### Referans

- [CLI](reference/cli.md): her `mistgate` ve `mistgate-node` komutu ve bayrağı.
- [Yapılandırma](reference/configuration.md): her `serve` ve `setup` bayrağı, her `MISTGATE_*` ortam değişkeni ve veri dizininin düzeni.
- [API](reference/api.md): Connect API, API token'ları ve profilleri.
- [MCP](reference/mcp.md): MCP sunucusu, stdio proxy'si, araçlar, plan / uygulama ve onaylar.
- [Mimari](reference/architecture.md): panelin, ajanların ve istemcilerin birbirleriyle nasıl konuştuğu.
- [SSS](reference/faq.md): sık sorulan sorulara kısa yanıtlar.
- [Diğer panellerle karşılaştırma](reference/comparison.md): Mistgate'in 3x-ui, Marzban, PasarGuard, Remnawave, Hiddify Manager ve s-ui ile yan yana karşılaştırması, kaynaklarıyla.
- [Performans testleri](reference/benchmarks.md): Mistgate'in belleği, işlemcisi, açılış süresi ve yükü; aynı küçük sunucuda 3x-ui, Remnawave ve PasarGuard ile karşılaştırmalı, yöntem ve sınırlarla birlikte.
