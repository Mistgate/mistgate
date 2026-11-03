---
title: İlk kullanıcılar
description: Tek bağlı düğümü olan bir panelden, bağlantısı çalışan bir kişiye giden en kısa yol.
---

Bir kişi, dört şey var olduktan sonra çalışan bir bağlantı alır: bağlı bir düğüm, o düğümde bir profil, profili olan bir grup ve o gruptaki bir kullanıcı. Bu sayfa onları bu sırayla ele alır. Bir düğümü elle ya da [SSH ile](ssh-install.md) [eklediğinizi](add-node.md) varsayar.

## Başlangıç kontrol listesi

Dördü de var olana kadar Genel bakış, aynı dört adımla bir **İlk adımlar** kartı gösterir:

1. **Düğüm bağlı**
2. **Düğümde profil**
3. **Profilli grup**
4. **Kullanıcı ve bağlantı**

Her adım panelde zaten olanlardan kendiliğinden işaretlenir; hiçbir şeyi elle işaretlemezsiniz. Bitmemiş bir adımın sizi oraya götüren bir düğmesi vardır ve sonraki adımın düğmesi vurgulanır. Her şey bittiğinde kart tek bir satıra, “Her şey hazır: bir düğüm, bir profil, bir grup ve bir kullanıcı” satırına katlanır; **Gizle** onu kaldırır (yalnızca bu cihazda).

## 1. Profil oluşturun ve düğüme koyun

**Profiller → Yeni profil** yolunu açın. Düğümün sayfasından da başlayabilirsiniz: **Profiller → Profil ekle**.

1. **Protokol.** Birini seçin:
   - Abonelik uygulamaları için **Hysteria2**: Happ gibi bir abonelik uygulaması ya da kl!ck, Clash Verge veya FlClash gibi mihomo çekirdekli bir uygulama. Tek bağlantı her sunucuyu taşır.
   - AmneziaVPN için **AmneziaWG**. Her cihaz kendi anahtarını alır. Kişinin AmneziaVPN'i açıksa mihomo çekirdekli uygulamalar bu sunucuları abonelik üzerinden de alır.
2. **Ad.** Siz bir tane yazana kadar porta uyar, örneğin “Hysteria2 · 443”.
3. **Ayarlar.** Varsayılanlar başlangıç için çalışır. Hysteria2 için bir alana bakın:
   - **Sertifika: Let's Encrypt** (varsayılan), A kaydı düğümü gösteren bir alan adı ve düğümde açık TCP 80 ile 443 gerektirir. Düğümün adresi kullanılır; doldurduysanız profilin **Alan adı (SNI)** değeri kullanılır.
   - **Sertifika: Kendinden imzalı (sabitlenmiş)** bir IP adresiyle çalışır; uygulama sertifikanın parmak izini kontrol eder, parmak izi abonelikle birlikte gider.

   Hysteria2, UDP 443 portunda Salamander gizlemesi ve üretilmiş bir parolayla başlar. Bir AmneziaWG profili, rastgele bir UDP portunda ve üretilmiş gizlemeyle 3.1 sürümü olarak başlar.
4. **Oluşturulur oluşturulmaz.** Alttaki bu blok sonraki adımları aynı geçişte yapar:
   - **Düğümlere dağıt**: düğümünüz zaten seçilidir.
   - **Gruplara ekle**: yeni bir panelde “Herkes” grubu işaretlidir, çünkü bu protokolden henüz bir profili yoktur.
5. **Profil oluştur** düğmesine basın. Panel “Profil oluşturuldu ve 1 düğüme dağıtıldı” der.

Düğüm sunucuyu birkaç saniye içinde başlatır. **Profiller** sekmesi portu, Alan adını (SNI) ve sertifikayı gösterir. Let's Encrypt sertifikası düğümde arka planda alınır ve verildiğinde bitiş tarihiyle birlikte orada görünür.

Seçili bir düğümde Let's Encrypt çalışamıyorsa form, profili oluşturmadan önce sizi uyarır: “adres bir IP, Let's Encrypt burada çalışmaz”. Bu durumda kendinden imzalı sertifikayı seçin ya da Alan adını (SNI) doldurun. Ayrıntılar: [Profiller](../guide/profiles.md), [Hysteria2](../guide/hysteria2.md), [AmneziaWG](../guide/amneziawg.md).

## 2. Grubu kontrol edin

**Kullanıcılar → Gruplar** yolunu açın. Kurulum sihirbazı “Herkes” grubunu oluşturdu; 1. adımda işareti bıraktıysanız profil zaten onun içindedir. Aksi halde grubu açın, **Gruptaki profiller** altında profili işaretleyin ve kaydedin. Profilin sayfasındaki **Nerede çalışıyor → Gruba ekle** aynısını yapar.

Profili olmayan bir grup hiç erişim vermez. Daha fazlası: [Kullanıcılar ve gruplar](../guide/users-and-groups.md).

## 3. Kullanıcıyı oluşturun

**Kullanıcılar → Yeni kullanıcı** yolunu açın:

| Alan | Varsayılan | Anlamı |
|:--|:--|:--|
| Ad | | Kişinin yönetim panelinde nasıl göründüğü, örneğin “Alice”. |
| Grup | | Kişinin profillerinin geldiği yer. |
| Kota | 100 GB | Dönem başına trafik. 0 sınırsız demektir. |
| Sıfırlama | Aylık | Her gün, hafta ya da ay (00:00 UTC) ya da hiç. |
| Süre | 30 gün | Sonrasında erişim duraklar. 0 süre sınırı yok demektir. |
| Cihazlar | 5 | Kaç cihaz bağlanabilir. |
| Abonelikteki uygulamalar | grubun verdiği | Abonelik uygulamaları ve AmneziaVPN, her biri bir anahtar. Grubun o uygulama için çalışan sunucusu yoksa anahtar kapalıdır. |
| Tüm düğümler | açık | Sonradan eklenenler dahil her düğüm. Düğüm seçmek için kapatın. |
| Daha fazla → DNS | grubun hazır ayarı | Yalnızca bu kullanıcı için bir DNS hazır ayarı. Bkz. [DNS](../guide/dns.md). |

**Oluştur** düğmesine basın. Panel “Alice oluşturuldu · bağlantı kopyalandı” der: abonelik bağlantısı artık panonuzdadır.

## 4. Bağlantıyı gönderin

Kullanıcının sayfasında bir **Abonelik bağlantısı** düğmesi vardır. Bağlantıyı ve QR kodunu **Bağlantıyı kopyala**, **Göndermek için kopyala**, **Paylaş**, **Sayfayı aç** ve **Yeni bağlantı** düğmeleriyle birlikte gösterir. Bağlantıyı özel bir mesajla gönderin: basit talimatlı bir sayfa açar.

Kişinin yaptığı:

1. Bağlantıyı bağlanmak istediği telefonda ya da bilgisayarda bir tarayıcıda açar. Sayfası o platform için talimatları, kurulacak uygulamaları, aboneliği uygulamaya ekleyen bir düğmeyi, başka bir cihaz için QR kodunu, trafiğini ve süresini gösterir. Ağ bant genişliğini düğümün **Sunucu** sekmesinde belirlediğiniz sunucular için sayfa ayrıca her sunucunun kanalının ne kadar meşgul olduğunu (Düşük, Orta, Yüksek) gösterir.
2. **Abonelik uygulaması:** uygulamayı kurar ve aboneliği sayfadaki düğmeyle ya da bağlantıyı uygulamaya yapıştırarak ekler. Uygulama sunucu listesini çeker ve sonra aynı bağlantıdan yeniler.
3. **AmneziaVPN:** **Abonelikler → Kullanıcı sayfası** bölümünde **Sayfada cihazlar** açıksa aynı sayfada cihaz için bir anahtar oluşturur ve AmneziaVPN'e aktarır. Kapalıysa anahtarı yönetim panelinde siz oluşturur ve gönderirsiniz.

Sayfa bir parola isterse sayfa parolası açıktır: **Göndermek için kopyala** bağlantıyı ve parolayı tek mesaja koyar. Sayfa onu cihaz başına bir kez sorar; uygulamaların buna hiç ihtiyacı yoktur.

Çalıştığını nasıl anlarsınız:

- Kullanıcının **Cihazlar** listesi, bir uygulama bağlantıyı çeker çekmez bir cihaz gösterir (o zamana kadar “Henüz yok — arkadaşın bağlantıyı açınca görünür”). Cihaz kimliği gönderen bir uygulama kendi satırını alır; diğerleri tek bir satırı paylaşır.
- Düğümün sayfası ve Genel bakış kişiyi çevrimiçi gösterir ve trafik saymaya başlar.
- Bağlantı penceresi kişinin “henüz hiçbir şey alamaz” olduğunu söylüyorsa, grubun kişinin kullanabileceği bir düğümde çalışan profili yoktur: 1. ve 2. adıma dönün.

> **Not:** **Yeni bağlantı** eski bağlantının yerini hemen alır; eskisini alan herkesin yenisine ihtiyacı olur. Bir bağlantı istediğinizden daha öteye paylaşıldıysa bunu yapın ve kullanıcının **Cihazlar** listesinde tanımadığınız cihazları silin.

## Sırada

- [Abonelikler](../guide/subscriptions.md) ve [Kullanıcı sayfası](../guide/user-page.md): her uygulamanın ne aldığı ve kişinin ne gördüğü.
- [Sağlık](../operations/health.md): istemci gözüyle kontroller yeni sunucuyu hemen test etmeye başlar.
- Bağlantıları başkalarına vermeden önce [Güvenlik](../operations/security.md).
