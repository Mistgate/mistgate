---
title: İstemci uygulamaları
description: "Her platformda hangi uygulamanın önerileceği, her birinin abonelikten ne aldığı, tek dokunuşla ekleme bağlantıları ve yedekleri ile uygulamaların yönetim panelinde ve MCP üzerinden nasıl ayarlanacağı. kl!ck, Mistgate'in önerdiği masaüstü uygulamasıdır."
---

Mistgate'in kendi istemcisi yoktur. İnsanlar mevcut uygulamalarla bağlanır ve panel her uygulamaya onun okuyabildiğini verir. Bu sayfa hangi uygulamanın hangi platforma uyduğunu, her birinin ne aldığını, [kullanıcı sayfasındaki](user-page.md) tek dokunuşluk **Ekle** düğmesinin nasıl çalıştığını ve uygulamaları nasıl ayarladığınızı anlatır.

## Hangi platformda hangi uygulama

| Platform | Önerilen | Ayrıca sunulan |
|:--|:--|:--|
| Windows, macOS | [kl!ck](#klck): Hysteria2, VLESS ve AmneziaWG tek uygulamada | Happ; anahtarlar için AmneziaVPN |
| iOS, Android | Happ: Hysteria2 | Anahtarlar için AmneziaVPN |
| Linux | varsayılan olarak yok | Anahtarlar için AmneziaVPN; eklediğiniz mihomo çekirdekli herhangi bir uygulama |

Bu, yeni bir panelin listesidir ([Yeni bir panel](#yeni-bir-panel) bölümüne bakın). Değiştirebilirsiniz: herhangi bir uygulama eklenebilir, kaldırılabilir, yeniden sıralanabilir ve önerilen olarak işaretlenebilir.

## Her uygulama ne alır

Tek bir abonelik bağlantısı her uygulamaya hizmet eder. Panel biçimi uygulamanın User-Agent değerine göre seçer (**Abonelikler → Kime ne verilir** kuralları, bkz. [Abonelikler](subscriptions.md)).

| Uygulama | Biçim | İçinde ne var |
|:--|:--|:--|
| kl!ck | Mihomo YAML (kl!ck `User-Agent: mihomo/…` gönderir, varsayılan `mihomo` kuralı bunu alır) | Hysteria2, VLESS ve AmneziaWG sunucuları, bir sunucu grubu, kişinin DNS hazır ayarından bir `dns:` bölümü. |
| Clash Verge Rev, FlClash, Android için Clash Meta | Uygulamanın User-Agent değeri `clash` ya da `mihomo` içeriyorsa Mihomo YAML (iki varsayılan kural) | kl!ck ile aynısı. Hiçbir kurala uymayan bir uygulama bağlantı listesini alır: User-Agent değerinin bir parçasıyla bir kural ekleyin. |
| Happ | Bağlantı listesi (base64) | Hysteria2 ve VLESS sunucuları: Happ AmneziaWG taşıyamaz. Yalnızca Happ ayrıca DNS hazır ayarıyla `Routing` başlığını ve sunucu adlarında yükü alır. |
| Hiddify | Mihomo YAML: User-Agent değeri `ClashMeta` içerir, varsayılan `clash` kuralı bunu `sing-box` kuralından önce alır | Mihomo YAML. Uygulamanın sunucularından hangisini kullandığı sürümüne bağlıdır. |
| v2rayNG, v2rayN ve Xray çekirdekli diğer uygulamalar | Bağlantı listesi (base64) ya da sizin bir kuralınızla Xray JSON | Hysteria2 ve VLESS sunucuları. Bağlantı listesinde uygulamanın `hysteria2://` ve `vless://` (REALITY, XHTTP) bağlantılarını okuyup okumadığı sürümüne bağlıdır. Xray JSON'da sunucu başına bir yapılandırma; AmneziaWG yok, Gecko'lu Hysteria2 yok. |
| sing-box uygulamaları (SFA, SFI, SFM) | sing-box JSON: User-Agent değerleri `SFA (sing-box …`, `SFI (sing-box …` ya da `SFM (sing-box …` ile başlar, varsayılan `sing-box` kuralı bunu alır | Hysteria2 (Let's Encrypt, Gecko yok) ve VLESS (TCP ya da WebSocket) sunucularından oluşan tek bir anahtar ve kişinin DNS'i. Bkz. [sing-box](#sing-box). |
| AmneziaVPN, AmneziaWG uygulamaları | Abonelik yok: kişinin sayfasından cihaz başına bir anahtar (`vpn://`, bir `.conf` dosyası ya da QR kodu) | Yalnızca AmneziaWG. |
| Telegram | Abonelik yok: kişinin sayfasından ya da yönetim panelinden bir `tg://proxy` bağlantısı | Yalnızca MTProto sunucuları, kişi başına bir anahtarla. Bkz. [Telegram](#telegram). |

AmneziaWG sunucuları mihomo çekirdekli bir uygulamaya yalnızca kişinin AmneziaVPN yolu açıksa (kullanıcı kartında ya da grupta **Abonelikteki uygulamalar**) ve grubun bir düğümde AmneziaWG profili varsa ulaşır. Böyle bir uygulamanın ilk çekişi cihazına AmneziaWG anahtarlarını verir. Çekirdek yeterince yeni olmalıdır: panel, her anahtarın yanında AmneziaWG 3.1 için Mihomo 1.19.30 ya da daha yenisini, 2.0 için 1.19.14'ü listeler ([AmneziaWG](amneziawg.md)).

## Hangi uygulamalar cihaz kimliği gönderir

[Cihaz sınırı](subscriptions.md#device-limit-for-apps-on-the-link) bir uygulamayı `X-HWID` başlığında gönderdiği cihaz kimliğine göre sayar. Kimliği olan uygulama kendi yerini ve anahtarlarını alır; kimliği olmayan bir uygulama, böyle olan diğer uygulamalarla bir yeri paylaşır. Son sütun, panel cihazı reddettiğinde kişinin gördüğüdür ([uygulamanın gösterdiği](subscriptions.md#what-the-app-shows-when-a-device-is-refused)).

| Uygulama | Cihaz kimliği gönderir | Reddedildiğinde ne gösterir |
|:--|:--|:--|
| kl!ck | Evet. Kimlik, bilgisayarın kimliği ile aboneliğin ana bilgisayarının, sistem ve bilgisayar adıyla birlikte özetidir. | Yalnızca yer tutucu girdi: duyuruyu okumaz. |
| Happ | Evet, varsayılan olarak. Kişi bunu uygulamanın ayarlarından kapatabilir; o zaman kimliği olmayan bir uygulama gibi sayılır. | Duyuru ve girdinin adı. |
| v2RayTun, INCY, FlClashX, Koala Clash, Prizrak-Box, RenoarX, DeskBox | Evet. | v2RayTun `X-Hwid-Limit` başlığını okur; diğerleri girdinin adını gösterir. |
| Shadowrocket, Karing, Clash Mi, Throne | Gönderebilir ama varsayılan olarak kapalıdır. | Girdinin adı. |
| Hiddify, v2rayN, v2rayNG, Clash Verge Rev, FlClash, sing-box uygulamaları, Passwall | Hayır (Temmuz 2026 itibarıyla; v2rayN, Hiddify ve Passwall'da bunun için istekler açık). | Girdinin adı. |
| Streisand, V2Box | Doğrulanmadı. | Girdinin adı. |

- Bu bilgi uygulamaların ve diğer panellerin belgelerinden ve kaynaklarından gelir, her uygulamanın testinden değil; yeni bir sürüm başlığı göndermeye başlayabilir ya da bırakabilir. Panel bir uygulamanın gönderdiğine göre hareket eder: kimlik göndermeyen bir uygulama kullanıcı kartında **Cihaz kimliği olmayan uygulamalar** altında görünür.
- Kimliği olmayan bir uygulamadaki kişi varsayılan kipte dışarıda kalmaz: bu uygulamalar tek bir yeri paylaşır. **Katı** kipte bağlanamazlar ve mesaj onları Happ'e ya da kl!ck'e yönlendirir.
- Panel, büyük harfli bir kimliği (INCY gönderir) ya da tiresiz bir kimliği (Happ'in masaüstündeki alternatif kimliği) olağan biçimiyle aynı cihaz olarak okur.

Xray JSON bir uygulamaya yalnızca bir kuralla ulaşır (**Abonelikler → Kime ne verilir**). v2rayNG onu okur, v2rayN'nin de 6.40 sürümünden itibaren okuduğu belgelenmiştir; v2RayTun, Streisand ve benzerleri için doğrulanmamıştır. Happ da onu okuyabilen olarak listelenir ama bağlantı listesinde kalır. Bir kural yazmadan önce uygulamayı bir abonelik bağlantısına `?format=xray-json` ekleyerek deneyin ([Abonelikler](subscriptions.md#who-gets-what) sayfasına bakın).

## Tek dokunuşla ekleme bağlantıları

Kullanıcı sayfasındaki **Ekle** düğmesi uygulamayı abonelik içinde hazır olarak açar. Bu, uygulamanın kendi bağlantı şemasıdır; abonelik bağlantısı uygulamanın **“Ekle” bağlantı şablonuna** yerleştirilir (yer tutucular `{url}`, `{url_enc}`, `{name_enc}`: bkz. [Abonelikler](subscriptions.md)). Şablon boşsa ya da uygulama bağlantıyı anlamıyorsa sayfa **Bağlantıyı kopyala** sunar: onu uygulamanın abonelik ekleme ekranına yapıştırın.

Bunlar bilinen uygulamaların şablonlarıdır. Her şema, Happ'inki dışında (aşağıdaki nota bakın), uygulamanın kendi belgesi ya da kaynak koduyla doğrulanmıştır.

| Uygulama | Platformlar | Şablon |
|:--|:--|:--|
| kl!ck | Windows, macOS | `klick://add?url={url_enc}&name={name_enc}` |
| Happ | iOS, Android, Windows, macOS | `happ://add/{url}` |
| Clash Verge Rev | Windows, macOS, Linux | `clash-verge://install-config?url={url_enc}&name={name_enc}` |
| FlClash | Android, Windows, macOS, Linux | `flclash://install-config?url={url_enc}` |
| Android için Clash Meta | Android | `clashmeta://install-config?url={url_enc}&name={name_enc}` |
| Hiddify | Android, iOS, Windows, macOS, Linux | `hiddify://import/{url}#{name_enc}` |
| v2rayNG | Android | `v2rayng://install-sub?url={url_enc}` |
| sing-box | iOS, Android, macOS | `sing-box://import-remote-profile?url={url_enc}#{name_enc}` |
| AmneziaVPN | hepsi | yok: abonelik değil, cihaz başına bir anahtar |

- Happ'in kendi belgesi `happ://add/` biçimini anlatmaz. Bu, diğer panellerin abonelik sayfalarının kullandığı biçimdir ve Mistgate'in varsayılan olarak gönderdiğidir. Düğme telefonda hiçbir şey yapmıyorsa kişi bağlantıyı kopyalar.
- Şemaları doğrulanmadığı için seçicide sunulmayanlar: Streisand ve Shadowrocket (şablonu boş bırakın, kişiler bağlantıyı yapıştırır).
- `clash://` şeması birkaç Clash uygulaması tarafından sahiplenilir; bu yüzden sistem başka birini açabilir. Yukarıdaki gibi uygulamanın kendi şemasını kullanın.

## Telegram

Telegram uygulaması [MTProto profilinin](mtproto.md) istemcisidir ve onu kullanan tek uygulamadır. Abonelik okumaz: kişi sunucu başına bir `tg://proxy` bağlantısı alır ve Telegram onu proxy olarak eklemeyi önerir. Bağlantılar kişinin sayfasındaki ([Kullanıcı sayfası](user-page.md#telegram-proxysi)) ve yönetim panelindeki kişinin **Telegram proxy'si** bloğundan gelir. Hiçbir zaman bir aboneliğe konmazlar; bu yüzden Happ, kl!ck ve diğer uygulamalar onları asla görmez.

Telegram uygulaması **Abonelikler → Kullanıcı sayfası** uygulama listesinde değildir: indirilecek ya da bir şablonla eklenecek bir şey yoktur. Kişi için yalnızca **Telegram proxy'si** anahtarının açık olması gerekir; bu, bağlantı uygulamalarına ya da AmneziaVPN'e ek olarak açılır ([Kullanıcılar ve gruplar](users-and-groups.md#telegram-proxy)). Protokol deneyseldir.

## kl!ck

[kl!ck](https://github.com/vbu00/klick), vbu00 tarafından yazılmış, değiştirilmemiş bir Mihomo çekirdeğinin etrafında Tauri ile Rust'ta geliştirilmiş, Windows ve macOS için açık kaynaklı bir masaüstü istemcisidir. Mistgate onu masaüstünde önerir ve yazarına teşekkür eder: uygulama Mistgate'in parçası değildir.

**Neden masaüstü önerisi.** Üç protokolü de taşıyan tek biçim olan Mihomo YAML'ini okur: Hysteria2, VLESS ve AmneziaWG'si olan bir kişi, tüm sunucularını tek abonelikten tek uygulamada, hazır ayarının DNS bölümüyle birlikte alır. Windows ve macOS'ta alternatif, Hysteria2 ve VLESS için Happ artı anahtarlar için AmneziaVPN'dir: iki uygulama ve ekleme için iki yol.

**Panelin ihtiyacı.** Özel bir şey yok: kl!ck aboneliği `mihomo/1.19.31` olarak çeker; bu yüzden varsayılan `mihomo` kuralı ona Mihomo YAML'ini verir. Uygulama girdisi **Abonelik bağlantısıyla** türündedir. AmneziaWG sunucuları için yukarıdaki anahtara bakın. kl!ck cihaz kimliği gönderir; bu yüzden kl!ck'li her bilgisayar cihaz sınırında kendi yerini alır.

**İndirme.** Sürümler sayfası: <https://github.com/vbu00/klick/releases/latest>. Windows ve macOS derlemeleri ayrı yayımlanır ve dosya adları sürümü taşır; bu yüzden panel bir dosyaya değil sayfaya bağlanır. Bu yazı yazılırken (Ekim 2026) son sürüm, Windows yükleyicili 0.4.0'dır; macOS portu kaynakta birleştirilmiştir ve macOS paketi yayımlandığında aynı sayfada görünür.

**Tek dokunuşla ekleme ve sınırı.** kl!ck `klick://add?url=<percent-encoded https link>&name=<optional name>` biçimini (ayrıca `klick://add/<link>`) işler. Kendiliğinden hiçbir şey eklemez: **Ekle** ekranını abonelik doldurulmuş olarak, bağlantının yalnızca alan adını göstererek açar ve kişi onaylar. Bağlantı `https://` olmalıdır. Bu işleyici kl!ck'e 0.4.0'dan sonra eklendi; bu yüzden **tek dokunuşluk ekleme 0.4.0'dan yeni bir kl!ck derlemesi gerektirir**. Daha eski derlemeler şemayı bilmez: kişi bağlantıyı sayfadan kopyalar ve kl!ck'e elle ekler.

## sing-box

Resmî uygulamalar, sing-box projesinden SFA (Android), SFI (iOS) ve SFM'dir (macOS). Windows ve Linux için resmî bir sing-box uygulaması yoktur; bu yüzden seçicide orada yoktur. Mistgate'in profili sing-box 1.12 ya da daha yenisini gerektirir.

**Panelin ihtiyacı.** Hiçbir şey: uygulamalar `SFA (sing-box 1.14.3; language en_US)` biçiminde bir User-Agent gönderir ve varsayılan `sing-box` kuralı onlara sing-box JSON'unu verir ([Abonelikler](subscriptions.md#who-gets-what) sayfasına bakın). Yeni bir panelin kullanıcı sayfasında yoktur; **Bilinen uygulama** → sing-box ile ekleyin.

**Ne alırlar.** Taşıyabildikleri her sunucuyla tek bir anahtar, ilki seçili olarak ve kişinin DNS hazır ayarı. Hysteria2 yalnızca Let's Encrypt sertifikasıyla ve Gecko olmadan; VLESS TCP (RAW + Vision) ya da WebSocket üzerinden. AmneziaWG yok, XHTTP üzerinden VLESS yok ve kendinden imzalı Hysteria2 yok: [ne nerede çalışır](subscriptions.md#what-works-where).

**Cihaz kimliği.** SFA ve SFI cihaz kimliği göndermez. Cihaz sınırı **Katı** olduğunda reddedilirler ve “This app does not report a device ID” görürler.

**İndirme.** Android: Google Play (<https://play.google.com/store/apps/details?id=io.nekohasekai.sfa>). iOS: App Store (<https://apps.apple.com/app/sing-box-mt/id6785326793>, “sing-box MT” adıyla). macOS: projenin sürümler sayfası (<https://github.com/SagerNet/sing-box/releases/latest>); SFM orada ayrı bir pakettir.

**Tek dokunuşla ekleme.** `sing-box://import-remote-profile?url=<percent-encoded https link>#<name>` aboneliği uzak bir profil olarak içe aktarır. Şema sing-box belgelerinden ve uygulamaların kaynaklarından gelir; henüz bir cihazda denenmemiştir.

## Uygulamaları yönetim panelinde ayarlama

**Abonelikler → Kullanıcı sayfası → Sayfadaki uygulamalar**, sayfanın gösterdiği kartların listesidir ([Abonelikler](subscriptions.md#apps-on-the-page) sayfasına bakın). Bir uygulama eklemek için:

1. Listenin altında bir **Platform** seçin.
2. Bir **Bilinen uygulama** seçin: kl!ck, Happ, AmneziaVPN, Clash Verge Rev, FlClash, Android için Clash Meta, Hiddify, v2rayNG ya da sing-box. Liste, o platformda bulunan uygulamaları içerir. **Kendi uygulaman, elle doldur** eski yolu korur: türü (**Kabul ettiği**) siz seçer ve alanları kendiniz doldurursunuz.
3. **Uygulama ekle** düğmesine basın. Kart ad, tür, o platformun indirme bağlantısı ve “ekle” bağlantı şablonuyla dolar. Herhangi bir alanı düzenleyin, bir açıklama (80 karakter) ekleyin, **Önerilen** anahtarını açın.
4. Kartı oklarla taşıyın: önerilen uygulama platformunda başa geçer, eşitler arasında listede ilk olan geçer. **Kaydet** düğmesine basın.

Var olan bir panelde kl!ck'i masaüstü önerisi yapmak için: onu Windows ve macOS için ekleyin, **Önerilen** anahtarını açın ve o iki platformda Happ'te kapatın. Sonucu aynı sekmedeki önizlemede görün.

## Yeni bir panel

Abonelik ayarlarını henüz kaydetmemiş bir panel şu varsayılanları gösterir:

- Windows ve macOS: önce kl!ck ve önerilen (sürümler sayfası, `klick://add?url={url_enc}&name={name_enc}`), sonra Happ.
- iOS ve Android: Happ, önerilen, `happ://add/{url}` ile.
- Beş platformun hepsi: anahtarlar için AmneziaVPN (telefonlarda mağazadan, diğerlerinde AmneziaVPN sitesinden).
- Üç kural: ikisi de Mihomo YAML'ine giden `mihomo` ve `clash`, ve sing-box JSON'una giden `sing-box`.

Varsayılanlar, kaydedilmemiş bir panelin gösterdiğinden ibarettir. Ayarları kaydettikten sonra panel listenizi tam kaydedildiği gibi tutar: bir güncelleme onu asla yeniden yazmaz.

## MCP üzerinden

Operatör ya da daha yüksek erişimi olan bir ajan aynı listeyi `subscription_settings_get`, `subscription_app_upsert_plan` / `subscription_app_upsert_apply` ve `subscription_app_remove_plan` / `subscription_app_remove_apply` ile yönetir. Sayfadaki her değişiklik sahibi bekler: sahip onu **Entegrasyonlar → Seni bekleyenler** bölümünde onaylar ve kart her alanı eksiksiz gösterir. kl!ck'i Windows ve macOS için ekleyen çalışılmış bir örnek [MCP](../reference/mcp.md#example-add-klck-for-windows-and-macos) sayfasındadır. [Yapay zekâ ajanı kılavuzunun](../getting-started/ai-agents.md) istemi bu adımı içerir.
