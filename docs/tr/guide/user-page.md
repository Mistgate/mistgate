---
title: Kullanıcı sayfası
description: Bir kişi bağlantısını tarayıcıda açtığında ne gördüğü, sayfa parolasının nasıl çalıştığı ve yöneticinin sayfayı nasıl önizlediği.
---

Kullanıcı sayfası, bir tarayıcının kişinin abonelik bağlantısında aldığı şeydir: o kişi için hazırlanmış, durumunu, trafiğini ve bağlanmak için basit adımları gösteren bir sayfa. İnsanları gönderdiğiniz sayfa budur; bir hesaba ya da yönetim paneline ihtiyaçları yoktur. Seçenekleri ve önerdiği uygulamalar **Abonelikler** bölümünün **Kullanıcı sayfası** sekmesinde ayarlanır ([Abonelikler](subscriptions.md) sayfasına bakın).

## Kişinin gördükleri

Karşılama, kullanıcının ayarlarındaki isteğe bağlı **Abonelik sayfasında görünen ad** alanını kullanır. Boş bırakırsanız **Kullanıcılar** bölümünde görünen hesap adı kullanılır. Bu herkese açık ad hesap adından ayrıdır ve kullanıcının erişimini ya da bağlantı bilgilerini değiştirmez.

Sayfa İngilizce, Rusça, Basitleştirilmiş Çince, Farsça ya da Türkçe konuşur: tarayıcının dil listesinde bunlardan ilki (herhangi bir Çince sayılır), yoksa panelin varsayılan dili. Farsça sağdan sola okunur ve tarihler Fars takvimiyle gösterilir. Üst çubuktaki bir anahtar dili değiştirir ve seçim o cihazda hatırlanır. Üst çubuk panelin adını ve logosunu ve markanın vurgu rengini taşır (**Ayarlar** → **Arayüz** bölümünde ayarlanır).

Yukarıdan aşağıya:

1. **Duyuru**, **Duyuruyu göster** açıksa. Kişi onu kapatabilir; yeni bir metin yeniden gösterilir.
2. **Karşılama ve durum**: “Merhaba, Alice”, durum (“Etkin”, “Sona erdi”, “Trafik bitti”, “Devre dışı”), sürenin kalan günleri ve dönemin trafiği kotaya karşı, sıfırlanma tarihiyle birlikte.
3. **Yük**: her sunucunun şu anda ne kadar meşgul olduğu, bir seviye olarak: **Düşük**, **Orta** ya da **Yüksek** (%50 ve %80'den itibaren): düğümün işlemci kullanımı ya da, ayarlıysa ve daha meşgulse, **Sunucu** sekmesindeki **Ağ bant genişliği** değerinin payı olarak kanalı. Ajanı taze bir örnek göndermeyen bir sunucu seviye göstermez. Bir sunucu ülkesi ve konumuyla adlandırılır, asla düğümün panelde görünen adıyla değil; ikisi de olmayan düğüm “Sunucu” olur ve tekrar eden ad bir numara alır. Sayfa hızları asla göstermez: bir düğümü bir başka kişiyle paylaşan biri, o kişinin ne zaman video izlediğini görmemelidir. Bir sunucu **Yüksek** olduğunda kart daha sakin birini önerir (“… sunucusu çok yoğun. Bağlantınız yavaşsa … sunucusunu deneyin.”).
4. **Bağlanma adımları** (ilk ziyaret; aynı akış, sonradan bir sayfa olarak, **Cihaz ekle**'dir). İki yol da varsa ilk adım **Neyle bağlanacaksınız**'dır: “Cihaz ekle” sayfasındakilerin aynısı iki kart. **Uygulamayla** önerilendir ve başlangıçta seçilidir; **AmneziaVPN anahtarıyla** diğeridir. Tek yol varsa soru yoktur.
   - **Cihazınız**: tarayıcıdan algılanan ve değiştirilebilen platform (iPhone, Android, Windows, Mac, Linux).
   - **Uygulamayla** (adlar uygulama listesinden gelir; yalnızca bağlantı uygulamaları listelenir, anahtar uygulamaları asla): uygulama, sonra onu kurmak (mağaza ya da indirme düğmesi ve kartın açıklamasıyla) ve **Happ'e ekle**, uygulamanın şablonundan gelen tek dokunuşluk bağlantı. Hemen altında “Açılmıyor mu? Bağlantıyı kopyalayın”. “Tüm sunucularınız (4) zaten orada — istediğinizi seçin”. **İkinci cihaz için QR** açıksa bir telefonu bağlamak için bağlantının QR kodu. “Uygulama aboneliği en son aldı” bağlantıdaki uygulamaların onu en son ne zaman çektiğini söyler. Sayfayı açmak bu zamanı güncellemez ve cihaz eklemez; yalnızca bir uygulamanın çekmesi güncelleme yapar.
   - **Anahtarla**: AmneziaVPN'i kurun, **Cihaz ekle** (anahtar formu açılır), anahtarı yapıştırın. Kendi kendine yönetim kapalıysa adım “Anahtar isteyin” der.
5. **Telegram proxy'si**, sahip Telegram'ı kişi için açtıysa, sunucuların altında ([Telegram proxy'si](#telegram-proxysi) bölümüne bakın).
6. **Cihazlarım**: bağlantıdaki uygulamaların ve kişinin anahtarlarının tek listesi ve her iki yol için tek bir **Cihaz ekle** düğmesi ([Cihazlarım](#cihazlarım) bölümüne bakın).
7. **Destek**: “Bir şey çalışmıyor mu?” ve, **Destek düğmesi** açıksa ve bir bağlantı ayarlıysa, destek bağlantısına giden bir düğme.
8. Altta bir satır: “Bu bağlantı size özel — kimseyle paylaşmayın. Abonelik etkin olduğu sürece çalışır.”

Uygulaması olmayan bir platform “… için henüz uygulama yok” iletisini ve bağlantıyı kopyalama ya da başka bir cihaz seçme önerisini alır. Henüz hiçbir şey almayan bir kişi “Sunucu hâlâ kuruluyor — yöneticiye yazın.” iletisini görür.

### Abonelik çalışmadığında

Süresi geçmiş, kotayı aşmış ya da devre dışı bırakılmış bir kişi adımlar yerine nedeni görür; bir bağlantı varsa destek düğmesiyle birlikte:

| Durum | Sayfa ne der |
|:--|:--|
| Süre bitti | “… üzerinden internet şu anda çalışmıyor. Bize yazın, uzatalım.” |
| Kota aşıldı, sıfırlamalı | “Bu ayki trafik bitti — VPN 1 Ekim tarihine kadar çalışmıyor. Daha önce gerekirse bize yazın.” |
| Kota aşıldı, sıfırlamasız | “Trafik sınırı doldu. Bize yazın, artıralım.” |
| Devre dışı | “Yönetici erişimi durdurdu. Bir yanlışlık varsa bize yazın.” |

Uygulamalar aynı nedeni aboneliğin tek girdisi olarak gösterir: bkz. [Abonelikler](subscriptions.md).

## Sayfa parolası

**Sayfada parola** açıkken (varsayılan) sayfa hiçbir şey göstermeden önce bir parola ister ve kendi kendine yönetim çağrıları da ona ihtiyaç duyar. Uygulamaların aynı adresten çektiği aboneliğin kendisi bunu asla istemez: uygulamalar parola yazamaz.

- Parola `k7m2-x9pq` gibi görünür: sekiz küçük harf ve rakam, birbirine benzeyen 0, 1, l ve o olmadan. Yazılırken büyük harfler, boşluklar ve tireler yok sayılır.
- Hiçbir yerde saklanmaz: panel onu kişinin bağlantısından türetir. Yeni bir bağlantı yeni bir parola demektir ve eski bağlantı ile parola çalışmayı bırakır.
- Yönetici onu bağlantı penceresinde bağlantının yanında görür (**Sayfa parolası**, kendi kopyalama düğmesiyle). **Göndermek için kopyala** bağlantıyı ve parolayı tek mesaj olarak birlikte kopyalar:

  ```ini
  Bağlantın: https://sub.example.com/<secret prefix>/<token>
  Sayfa parolası: k7m2-x9pq
  ```

- Sayfa tarayıcı başına bir kez sorar ve yaklaşık 180 gün hatırlar (bağlantının yoluyla sınırlı bir çerez).
- Bir bağlantı için ya da bir ağdan 10 dakika içinde beş yanlış deneme, formu 10 dakikanın geri kalanı için kilitler (“Çok fazla deneme. 7 dk sonra tekrar deneyin.”). Kilitlenme, bağlantı ya da adres olmadan, kişinin adıyla denetim günlüğüne yazılır.
- Form olağan hatalarda yardımcı olur: Latin olmayan harfler ya da Türkçe karakterler (“Şifrede yalnızca Latin harfleri ve rakamlar var: ı, ş, ğ, ü, ö, ç yerine i, s, g, u, o, c yazın.”) ve yanlış uzunluk (“Şifre 8 karakterdir, siz 7 karakter girdiniz.”).

Bağlantıları yalnızca onlara güvendiğiniz kişilere gönderiyorsanız seçeneği **Kullanıcı sayfası** sekmesinde kapatın; o zaman bağlantıya sahip olan herkes sayfayı görür.

## QR kodları

- **Yönetim panelinde**: kullanıcı kartındaki **Abonelik bağlantısı**, bağlantının QR kodunu gösterir. Bir telefon kamerasıyla taranınca sayfayı açar.
- **Sayfada**: **İkinci cihaz için QR** açıkken sayfa, başka bir cihaz için bağlantının QR kodunu gösterir: bilgisayarda adımların yanında, telefonda “Başka bir cihaz bağlayın” altında katlanmış olarak.
- **Anahtar için**: anahtar penceresi, AmneziaVPN ya da AmneziaWG uygulamalarında taranacak `.conf` dosyasının QR kodunu gösterir. QUIC görünümlü bir yapılandırma QR kodu için çok uzundur: “Anahtar QR kodu için çok uzun. Dosyayı indirin ya da anahtarı kopyalayın.”

## Platforma göre talimatlar

Adımlar **Kullanıcı sayfası** sekmesindeki uygulama listesinden gelir. Seçilen platform için her bağlantı uygulaması bir kart alır (anahtar uygulamaları asla aralarında olmaz: anahtar yolunun kendi adımları vardır):

- önerilen uygulama, birden fazla olduğunda bir “Önerilen” rozetiyle yolun başında yer alır;
- indirme düğmesi mümkünse mağazayı adlandırır (App Store, Google Play), yoksa “web sitesinden” der;
- kartın açıklaması, ayarlıysa;
- “ekle” bağlantı şablonundan gelen ekleme düğmesi ya da şablon yoksa, nereye yapıştırılacağıyla birlikte “Bağlantıyı kopyala” (“Uygulamada: abonelik ekle → bağlantıyı yapıştır”).

Anahtarlar için sayfa AmneziaVPN adımlarını verir: telefonda “AmneziaVPN uygulamasını açın”, ““+” düğmesine dokunun ve anahtarı yapıştırın”, ““Devam” düğmesine dokunun”; bilgisayarda “AmneziaVPN → “+” → “Bağlantı ayarları dosyası” → indirilen dosyayı seçin”, anahtarı değil dosyayı ekleme uyarısıyla. “Gerekli: AmneziaVPN 5.0.1.5 veya daha yenisi”, anahtarın protokol sürümü için en eski istemciyi adlandırır.

## Telegram proxy'si

Sahip **Telegram proxy'si** seçeneğini kişi için açtığında ([Kullanıcılar ve gruplar](users-and-groups.md#telegram-proxy) sayfasına bakın), sayfada “VPN uygulaması olmadan, proxy üzerinden Telegram'a bağlanın” satırıyla bir **Telegram proxy'si** bloğu bulunur. Yalnızca Telegram uygulaması içindir ve yukarıdaki adımlardan bağımsızdır.

- Her satır bir [MTProto](mtproto.md) bağlantısıdır; sayfanın diğer sunucuları gibi sunucunun ülkesi ve yeriyle adlandırılır, asla düğümün ya da profilin panelde görünen adıyla değil. Düğümün IPv6 adresindeki ikinci bir bağlantı **· IPv6** ile, bir aktarıcı üzerinden olan **· RU üzerinden** ile biter (girişin ülke kodu, sayfanın dilinde).
- **Telegram'da aç** bağlantıyı Telegram uygulamasında açar; uygulama kişiden proxy'yi onaylamasını ister. **Bağlantıyı kopyala** onu bir sohbete ya da Telegram'ın proxy ayarlarına yapıştırmak üzere kopyalar.
- Sahip kişinin anahtarını oluşturmadan önce ya da onun için hiçbir MTProto sunucusu çalışmazken blok “Proxy hâlâ hazırlanıyor — yöneticiye yazın.” der. Telegram'ı kapalı olan kişinin bloğu yoktur.
- Kişi anahtarı sayfada yeniden veremez ya da kaldıramaz. Sahip bunu yönetim panelinde yapar.

Bağlantılar kişinin anahtarını taşır; bu yüzden abonelik bağlantısı kadar özeldir.

## Cihazlarım

Bir kart, kişinin cihaz sınırından bir yer alan her şeyi barındırır:

- **Cihaz kimliği gönderen bağlantı uygulaması** (Happ, kl!ck ve [İstemci uygulamaları](client-apps.md#hangi-uygulamalar-cihaz-kimliği-gönderir) sayfasındaki diğerleri): kendi satırı ve kendi yeri. Satırda ad (model, ya da kişinin ya da sahibin verdiği ad; yoksa “iPhone” gibi sistem) ve tek bir sakin satır bulunur: “çevrimiçi” ya da “5 dakika önce güncellendi”. Uygulamanın bildirdiğini görmek için satıra basın: sistem ve sürümleriyle uygulama (“iOS 18.1 · Happ 3.4.2”). Çevrimiçi olan uygulamalar önce gelir, sonra en son güncellenenler; böylece kişinin elindeki cihaz en üsttedir. Dörtten fazla satırlık bir liste üçe katlanır ve **N tane daha göster** düğmesi çıkar; tüm yerler kullanılmışsa hariç: o zaman kişi kaldıracağı eski cihazı arıyordur.
- **Cihaz kimliği olmayan uygulamalar** (Hiddify, v2rayNG ve kimlik göndermeyen diğer uygulamalar ya da HWID kapalı Happ): hepsi birlikte tek bir satır, “hepsi için tek yer”. Kaldırılamaz ya da yeniden adlandırılamaz; son uygulamayı görmek için ona basın. **Katı** kipte bu uygulamalar bağlanmaz ve satır bunu söyler: “bağlanamaz — Happ veya kl!ck gerekir”.
- **Anahtarlar**: her AmneziaVPN anahtarı için bir satır, her biri bir yer, **Anahtarı göster** ve **⋯** menüsüyle (**Yeniden adlandır**, **Anahtarı değiştir**, **Kaldır**).
- Başlıktaki sayaç (“5 / 10”) ve kişinin her iki yolu ve bir sınırı varsa, neyin yer kapladığını söyleyen bir satır.

**Sınırla** ve **Katı** kiplerinde sınır dolduğunda başlığın altındaki sakin bir satır “Tüm yerler dolu. Eski bir cihazı kaldırın, sonra uygulamada aboneliği güncelleyin.” der. Uygulama o zaman hemen bağlanır. **Yalnızca say** kipinde kimse reddedilmez ve satır gösterilmez.

Bağlantıdaki bir uygulamanın **⋯** menüsünde **Yeniden adlandır** (en fazla 40 karakter; sahip adı da görür) ve **Kaldır** vardır. Kaldırmadan önce sayfa uygulamanın bildirdiğini gösterir; böylece birbirinin aynı iki telefon ayırt edilebilir. Kaldırmadan sonra o cihazdaki uygulama bağlanmayı bırakır ve yeri boşalır. Boş bir yer varken aboneliği yeniden yenilerse yeni anahtarlarla yeni bir cihaz olarak geri gelir. Engelleme sayfada sunulmaz: sahip içindir ve engellenen bir cihaz listede görünmez. **Sayfada cihazlar** olmadan ve sahibin önizlemesinde **⋯** menüsü yoktur.

**Cihaz ekle** yanındaki bir düğme bağlantıyı tek dokunuşta kopyalar. Kişi onu her cihaza yapıştırır: cihaz kimliği gönderen her uygulama kendi yerini alır, diğerleri tek bir yeri paylaşır.

Listenin altında tek bir **Cihaz ekle** düğmesi vardır (kartın altında duran “Başka bir cihaz bağlayın” bloğu artık yok). İki yol da varsa “Nasıl bağlanacak?” diye soran bir sayfa açar:

- **Uygulamayla** (önerilen): bir bağlantı uygulamasını bağlama adımları: bu cihaz, uygulama (platformun bağlantı uygulamaları, uygulama listesinden), “Tek dokunuşla ekle” ya da “Bağlantıyı kopyala” ve **İkinci cihaz için QR** açıksa başka bir cihaz için sayfanın QR kodu. Kart “1 yer kaplar” ve kaç yerin boş olduğunu söyler: cihaz kimliği gönderen bir uygulama kendi yerini alır.
- **Anahtarla**: yeni bir anahtarın formu (aşağıya bakın), kartıyla aynı başlık ve anahtar simgesi altında. Bir yer kaplar ve kaç tanesinin boş olduğunu söyler.

Kişinin kullanamadığı bir yol, nedeniyle birlikte sayfada sakin bir kart olarak kalır: tüm yerler kullanılmış (ve bir destek bağlantısı varsa **Yazın**), anahtarlar sahip tarafından veriliyor, henüz bağlantı seçeneği yok ya da sahibin önizlemesi (çağrılar için adresi yoktur). Bağlantı yolu bunların hepsinde çalışmaya devam eder. Tek yol varsa soru atlanır: yalnızca bağlantısı olan kişi doğrudan adımlarına, yalnızca anahtarları olan anahtar formuna gider.

## Kendi kendine cihaz yönetimi

**Sayfada cihazlar** açıkken (varsayılan) kişi kendi cihazlarını yönetir: bağlantıdaki uygulamalar yeniden adlandırılabilir ve kaldırılabilir ([Cihazlarım](#cihazlarım) bölümüne bakın), AmneziaVPN anahtarları ise şöyle yönetilir. Tüm değişiklikler birlikte bağlantı başına saatte 20 ile sınırlıdır; ötesinde sayfa kaç dakika sonra yeniden deneneceğini söyler.

- **Cihaz ekle** önce cihazın nasıl bağlanacağını sorar ([Cihazlarım](#cihazlarım) bölümüne bakın); **Anahtarla** yolu bir formdur: “Bağlantı seçeneği” (ana olan düğümlerinin ülkeleriyle adlandırılır; bir WARP profili “WARP (Cloudflare üzerinden çıkış)”; bir 2.0 profili “Eski AmneziaVPN sürümleri için (5.0.1.5 öncesi)”), “Ne tür bir cihaz” ve “Ad (isteğe bağlı)”. Anahtar hemen açılır.
- Her cihaz için: ana eylem olarak **Anahtarı göster** ve **Yeniden adlandır**, **Anahtarı değiştir** (eskisi hemen çalışmayı bırakır) ve **Kaldır** (üzerindeki VPN hemen durur) içeren **⋯** menüsü. Kaldırma ve değiştirme önce satırın içinde sorar.
- Anahtar penceresi: **Ülke** (düğüm başına bir bağlantı; “Her ülke ayrı bir bağlantıdır”), **Anahtarı kopyala**, **Dosyayı indir**, adımlar ve bir QR kodu. Bir sunucu **Yük** kartında ve kişinin uygulamasında olduğu gibi adlandırılır: ülkesi ve konumuyla, asla düğümün panelde görünen adıyla değil; ikisi de olmayan düğüm “Sunucu” olur ve tekrar eden ad bir numara alır (“Almanya 2”). Anahtarın AmneziaVPN'deki bağlantısı, abonelik başlığından sonra aynı adı taşır (“Mistgate · Almanya 2”).
- Eski bir anahtar çalışmayı bıraktığında (profilin kritik bir değişikliği, bir düğümün yeni portu ya da adresi) cihaz “yeni anahtar gerekli” der ve **Yeni anahtar al** kişiye değiştirme adımlarını gösterir.

Sınırlar: yalnızca etkin bir kişi cihaz ekleyebilir, anahtar gösterebilir ya da değiştirebilir; cihaz sınırı geçerlidir (“Tüm yerler dolu. Artık kullanmadığınız bir cihazı kaldırın ya da bize yazın — yer ekleyelim.”); ve bir bağlantı saatte 20 böyle istek yapabilir, anahtar göstermek dahil. Seçenek kapalıyken sayfa anahtarları listeler ve “Anahtarları yönetici verir — yenisi gerekirse bize yazın.” der.

Aboneliği etkin olmayan bir kişi AmneziaVPN anahtarlarını yine görür ve onları yeniden adlandırıp kaldırabilir; yeni anahtar eklemek mümkün değildir.

## DNS

Sahip **Sayfada DNS seçimi** seçeneğini açtığında (aşağıya bakın), kişi sayfasında şunları seçer:

- **bağlantıdaki uygulamalar için bir DNS** (Happ, mihomo çekirdekli uygulamalar, Xray JSON ve sing-box uygulamaları), sahibin anahtarın altında listelediği hazır ayarlardan: **Sunucular** bölümünün en üstünde, uygulamalara göre adlandırılmış bir kart, “Happ ve bağlantıyı kullanan diğer uygulamalarda DNS”. Uygulamalar yeni DNS'i bir sonraki abonelik güncellemelerinde, yenileme aralığı içinde alır ve kart bunu söyler;
- **her sunucunun AmneziaWG anahtarlarının DNS'i**, sahibin o düğümde sunduğu hazır ayarlardan: sunucunun kartında anahtar simgeli bir **Anahtar DNS'i** satırı.

İkisi asla karışmaz: birincisi anahtarlara ulaşmaz, ikincisi bağlantıdaki uygulamalara ulaşmaz. **Anahtar DNS'i** satırları yalnızca AmneziaVPN anahtarlarını kullanan bir kişiye (uygulama onun için açıktır ya da bir anahtarı vardır) ve anahtar taşıyan sunucularda gösterilir; ikisini de kullanan kişi, kartın altında anahtarların kendi DNS'ini koruduğunu söyleyen bir satır okur. Sahip bağlantıdaki uygulamalar için hiçbir şey sunmuyorsa, anahtarları olan bir kişi bunun yerine bağlantıdaki uygulamaların tüm sunucular için tek bir DNS'i olduğunu ve hangisi olduğunu okur. Hazır ayarlar ve hangi DNS'in geçerli olduğuna ilişkin kural [DNS](dns.md) sayfasındadır; bu bölüm sayfanın onunla ne yaptığını kapsar.

Hazır ayarların adları ve açıklamaları, tarayıcınınkinden farklı olduğunda bile sayfanın dilini izler: İngilizce bir tarayıcıda sayfayı Rusçaya geçiren kişi yerleşik hazır ayarları Rusça okur. Sahibin yeniden adlandırdığı ya da oluşturduğu bir hazır ayar her dilde kendi metnini gösterir.

### Sayfanın bir sunucu hakkında bildikleri

Sayfa, kişinin her sunucusu için şunları alır:

- ülke ve konum, asla düğümün panelde görünen adı değil;
- düğümün yanıt verip vermediği (yanıt vermeyen sunucu “yanıt vermiyor” gösterir);
- yük seviyesi: yüzdesiz **Düşük**, **Orta** ya da **Yüksek** (yukarıdaki **Yük** bölümüne bakın);
- sunucunun bağlantıdaki uygulamalarda taşıdığı ad. **Sunucu adları** şablonu ([Abonelikler](subscriptions.md) sayfasına bakın) `{node}` içeriyorsa o ad sayfaya gönderilmez; böylece düğümün adı onun üzerinden sızmaz;
- sunucudaki bir bağlantının yalnızca Mihomo uygulamaları için olup olmadığı. Gecko gizlemesini ([Hysteria2](hysteria2.md) sayfasına bakın) yalnızca kl!ck ve Mihomo çekirdekli diğer uygulamalar konuşur; bu yüzden Happ ve bağlantı listesi kullanan diğer uygulamalar böyle bir bağlantı almaz. Sayfa sunucuyu yine listeler ve kartında “Yalnızca kl!ck ve diğer Mihomo uygulamalarında” der. Her sunucusu böyle olan bir kişi, bağlantı listesi uygulamalarında boş bir liste yerine aynı şeyi söyleyen tek bir girdi alır.

### Seçme ve uygulama

- Bağlantıdaki uygulamalar için seçim denetim günlüğüne `page_link_dns_choice` olarak yazılır: kişinin adı ve hazır ayar. Bir sunucunun anahtarları için seçim sunucu sunucu yapılır ve `page_dns_choice` olarak yazılır: kişinin adı, düğümün adı ve hazır ayar. İkisinde de bağlantı ya da adres yoktur.
- Sayfadaki diğer eylemlerle aynı sınıra sayılır: bağlantı başına saatte 20 değişiklik.
- Nerede çalıştığı biçime göre değişir:
  - **AmneziaVPN ve AmneziaWG anahtarları** (`.conf`, `vpn://`, QR kodu): DNS anahtarın içine yazılır; bu yüzden kişinin yeniden **Yeni anahtar al** düğmesine basması gerekir. Anahtarın kendisi değişmez; kişi basana kadar cihaz DNS nedeniyle “yeni anahtar gerekli” olarak işaretlenir. İşaret, anahtarın taşıdığıyla o sunucuda şu anda kişi için geçerli olanı karşılaştırır, neyin değiştirdiğinden bağımsız: kişinin seçimi ya da varsayılana dönmesi, sahibin sunulan hazır ayarları ya da varsayılanı değiştirmesi, düzenlenen ya da silinen bir hazır ayar. Anahtar yalnızca kişinin kendi cihazı onu çektiğinde alınmış sayılır: bir yöneticinin anahtarı panelde açması işareti kaldırmaz. Bu izlenmeden önce yapılmış bir anahtar, kişi onu bir kez çekene kadar işaret almaz.
  - **Bir Mihomo profilinin AmneziaWG proxy'leri** (Clash Verge Rev, FlClash ve benzerleri): seçim bir sonraki abonelik güncellemesinde uygulanır.
  - **Sunucu başına değil**: Mihomo'daki Hysteria2, Happ, Xray JSON ve sing-box tüm abonelik için tek bir çözümleyiciye sahiptir; bu yüzden bağlantıdaki uygulamaların DNS'ini kullanırlar: kişinin onlar için kendi seçimi ya da olağan hazır ayar. Sunucu başına bir seçim onlara ulaşmaz. base64 biçimindeki diğer uygulamalar (v2rayNG, Hiddify, Streisand, Shadowrocket) panelden hiç DNS almaz.
- Sahibin önizlemesinde, sayfadaki diğer eylemler gibi seçim değiştirilemez.

### Seçenek

**Abonelikler** → **Kullanıcı sayfası** → **Sayfada DNS seçimi** (varsayılan olarak kapalı; seçenek var olmadan önce kurulmuş bir panel kapalı sayılır). Açıkken anahtarın altındaki **Bağlantıdaki uygulamalar için DNS**, bir kişinin onlar için seçebileceği hazır ayarları ve varsayılanı listeler; her düğümün anahtarları için sunduğu hazır ayarlar düğümde ayarlanır. Seçeneği kapatmak insanların zaten yaptığı seçimleri silmez: siz onları kullanıcı kartında sıfırlayana kadar çalışmaya devam ederler ([Kullanıcılar ve gruplar](users-and-groups.md) sayfasına bakın).

## Yönetim panelindeki önizleme

**Abonelikler** → **Kullanıcı sayfası**, sayfayı ayarların yanında bir telefon çerçevesinde gösterir. **Şu kullanıcı olarak önizle** kullanıcıyı seçer (ilk 50'den); yenile düğmesi onu tazeler.

- Önizleme kaydedilmiş olanı gösterir: kaydedin, yeniden yüklenir.
- Bu, o kullanıcının gerçek bağlantısıyla, yönetim panelinin oturumunuzun arkasında `/preview/user-page/<user id>` altında sunduğu gerçek sayfasıdır. Sayfa parolasını sormaz ve bağlantının bir çekilmesi sayılmaz.
- Üzerinde hiçbir şey yazamaz: kendi kendine yönetim düğmelerinin çağıracağı bir şey yoktur.
- Onu yalnızca kullanıcıların bağlantılarını görebilen yöneticiler açabilir: sahip ve yardımcılar.

## Sayfayı kimler açabilir

Bağlantıya sahip olan herkes ve sayfa istiyorsa parola. Sayfa arama motorlarının dizinlememesi için işaretlenmiştir, başka bir sitenin çerçevesinde gösterilemez ve başka hiçbir yerden bir şey yüklemez. Bir bağlantıya bir anahtar gibi davranın: sızarsa kullanıcı kartından yeni bir tane verin.
