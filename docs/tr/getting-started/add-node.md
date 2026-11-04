---
title: Düğüm ekleme
description: SSH ile kurulum ve elle kurulum arasında seçim yapın, bir sunucuyu tek kullanımlık bir komutla kaydedin, çevrimiçi olmasını izleyin ve sonra temiz biçimde kaldırın.
---

Bir düğüm, ajanı panelden sertifika alıp kendini bir systemd hizmeti olarak kurduğunda ve bağlandığında filoya katılır. **Düğümler → Düğüm ekle** iki yol sunar:

| Yol | Ne yaparsınız | Ne zaman seçilir |
|:--|:--|:--|
| **SSH ile otomatik kur** | Sunucunun adresini girer, SSH ana bilgisayar anahtarını onaylar, panel anahtarıyla ya da parolayla giriş yaparsınız; gerisini panel yapar. | Olağan yol. [SSH ile düğüm kurma](ssh-install.md) sayfasında anlatılır. |
| **Elle kurulum için komut al** | Ajanı sunucuya kopyalar ve root olarak tek kullanımlık bir komut çalıştırırsınız. | Panel sunucuya SSH ile ulaşamıyorsa (Cloudflare sürümünde: adres Cloudflare'e aittir ya da onun proxy'sinin arkasındadır, ağ özeldir ya da SSH yalnızca kendi IP'nize açıktır) ya da komutları kendiniz çalıştırmak istiyorsanız. Bu sayfada anlatılır. |

Bir yapay zekâ ajanı da MCP üzerinden bir SSH kurulumu başlatabilir; onaylarsınız ve onay ekranında panel anahtarını seçersiniz ya da parolayı yazarsınız: bkz. [Yapay zekâ ajanı kılavuzu](ai-agents.md). Bu sayfa ayrıca bir düğümün nasıl kaldırılacağını da anlatır. Pencere doğrudan ad, ülke ve adres alanlarıyla açılıyorsa panel SSH kurulumu gelmeden önceki bir sürümdür: önce onu güncelleyin ([panel güncellemelerine](../operations/updates.md) bakın).

## Elle kurulumdan önce

- [Gereksinimleri](requirements.md) karşılayan, SSH ile root erişimi olan bir sunucu.
- Panelle aynı sürümden ajan ikili dosyası: `mistgate-node-linux-amd64` ya da `mistgate-node-linux-arm64`. Güvenilir bir sürüm paketi olan bir panel bunun için hazır bir `scp` komutu yazdırır; yoksa onu panelle aynı [GitHub sürümünden](https://github.com/Mistgate/mistgate/releases/latest) ya da kendi derlemenizin `bin/` dizininden alın.
- Panele sunucudan herkese açık adresinde TCP 443 üzerinden (ya da `serve --agent-addr` ile verdiğiniz adresten) ulaşılabilmelidir. Panel düğüme asla kendisi bağlanmaz.

## 1. Elle kurulumu seçin

**Düğümler → Düğüm ekle** bölümünde **Elle kurulum için komut al** seçeneğini seçin. Aynı pencere Genel bakış'taki **Düğüm ekle** kutucuğundan, komut paletinden ve kurulum sihirbazının sonundan da açılır. Bekleyen bir elle düğüm oluşturduysanız, aynı adı SSH sihirbazında kullanmadan önce onu kullanımdan kaldırın ya da kullanılmayan başka bir ad seçin.

| Alan | Ne girilir |
|:--|:--|
| Ad | a–z, 0–9 ve tire, 2 ila 24 karakter; örneğin `de1`. Adlar canlı düğümler ve etkin SSH kurulumları arasında benzersizdir; bir düğümü kullanımdan kaldırmak geçmişini korurken adını serbest bırakır. |
| Ülke | İsteğe bağlı. DNS için önemlidir: Rusya'daki düğümlerde Teşhis gosuslugi.ru'yu kontrol eder ve Yandex DNS önerir. |
| Adres | `https://` ve port olmadan bir alan adı ya da IP. İstemciler ona bağlanır ve her aboneliğe girer. Hysteria2 için bir Let's Encrypt sertifikası, A kaydı sunucuyu gösteren bir alan adı gerektirir. |

Adres bir IP olduğunda pencere, siz devam etmeden önce bunu söyler: o düğümdeki Hysteria2 için, profili eklerken seçeceğiniz A kaydı olan bir alan adı ya da kendinden imzalı (sabitlenmiş) sertifika gerekir. AmneziaWG bir IP üzerinde olduğu gibi çalışır.

**Kurulum komutu oluştur** düğmesine basın. Düğüm listede “Kurulum bekliyor” olarak görünür ve pencere üç adım gösterir.

## 2. Ajanı sunucuya koyun

Panel veri dizininde güvenilir bir sürüm paketi tutuyorsa (resmî sürümden bir panel onu kendiliğinden indirir), pencerenin 1. adımı panelin sunucusunda çalıştırmanız için hazır bir `scp` komutu gösterir. Bu komut amd64 ajanını kopyalar; ARM sunucu için `amd64` yerine `arm64` yazın.

Aksi halde aynı sürümün ajanını `/root/mistgate-node` konumuna kendiniz kopyalayın, örneğin derleme makinenizden:

```sh
scp bin/mistgate-node-linux-amd64 root@de1.example.com:/root/mistgate-node
```

## 3. Komutu root olarak çalıştırın

Sunucuya SSH ile root olarak girin ve pencerenin 2. adımındaki komutu yapıştırın (**Komutu kopyala**). Şuna benzer:

```sh
chmod +x /root/mistgate-node && /root/mistgate-node enroll --panel panel.example.com:443 --sni <secret name> \
  --ca-sha256 <fingerprint> --token <one-time token> && /root/mistgate-node install
```

| Parça | Anlamı |
|:--|:--|
| `--panel` | Ajanın bağlandığı ana bilgisayar ve port. |
| `--sni` | Ajan uç noktasına götüren gizli TLS adı. O olmadan panel kamuflaj sitesini gösterir. |
| `--ca-sha256` | Panel CA'sının parmak izi. Düğüm panele yalnızca onunla güvenir. Pencere onu **Ayrıntılar** altında da gösterir. |
| `--token` | Tek kullanımlık kayıt token'ı. Bir kez çalışır ve bir saat sonra geçerliliğini yitirir; pencere ne zamana kadar geçerli olduğunu gösterir. |

Başarılı olduğunda şunu yazdırır:

```text
enrolled as nod_... with panel.example.com:443; state in /var/lib/mistgate-node
next: mistgate-node install   (or, in the foreground: mistgate-node run)
installed /etc/systemd/system/mistgate-node.service (state /var/lib/mistgate-node)
started; follow it with: journalctl -u mistgate-node -f
```

> **Not:** `enroll` çalışırken token sunucunun süreç listesinde görünür. Tek kullanımlıktır ve ömrü kısadır; sunucuyu başkalarıyla paylaşıyorsanız `--token` yerine `MISTGATE_ENROLL_TOKEN` ayarlayın ya da token'ı `--token-stdin` ile standart girdiden verin.

## Sunucuda neler olur

`enroll`:

1. Düğümde bir P-256 anahtarı ve sertifika isteği üretir. Anahtar sunucudan asla çıkmaz.
2. `--panel` adresine gizli adla TLS 1.3 üzerinden bağlanır ve sunucuyu yalnızca zinciri sabitlenmiş parmak izli bir CA'da bitiyorsa kabul eder.
3. Token'ı ve isteği gönderir. Panel token'ı kontrol eder (yalnızca özetini saklar), 30 gün geçerli bir istemci sertifikası verir ve CA'sıyla birlikte geri döndürür.
4. Anahtarı ve sertifikayı (`identity.pem`), panel CA'sını (`ca.pem`) ve panel adresini (`agent.json`) `/var/lib/mistgate-node` içine yazar (dizin 0700, dosyalar 0600).

`install`:

1. Root olarak çalıştığını ve durum dizininde bir kimlik bulunduğunu kontrol eder.
2. Kendini `/usr/local/bin/mistgate-node` konumuna kopyalar. `/root` içindeki dosyaya artık gerek yoktur.
3. Sunucunun belleğine göre boyutlandırılmış sertleştirilmiş `/etc/systemd/system/mistgate-node.service` birimini yazar, ardından `systemctl daemon-reload`, `enable` ve `restart` çalıştırır.

Başladıktan sonra ajan:

1. Ana bilgisayar temelini uygular: fq ve BBR sysctl değerleri, en az 16 MiB olan UDP soket tamponu üst sınırları (`net.core.rmem_max` ve `net.core.wmem_max`), journald boyut sınırı ve kendi nftables tablosundaki SSH koruması. Yeni UDP aktarma soketleri `net.core.rmem_default` ve `net.core.wmem_default` değerlerinden en az 1 MiB miras alır; toplam UDP belleğini yine `net.ipv4.udp_mem` sınırlar. Conntrack tablosu en az 65.536 girdi ve 16.384 kova alır, daha yüksek canlı değerler korunur.
2. Son uygulanan durumu durum dizininden geri getirir (ilk başlangıçta hiçbir şey yoktur).
3. Panele karşılıklı TLS ile uzun ömürlü tek bir bağlantı açar, ana bilgisayarın bilgilerini bildirir ve ayarlarını ile istenen durumu alır: hangi sunucuların hangi kullanıcıların kimlik bilgileriyle çalışacağı.
4. Trafiği, oturumları ve ana bilgisayar metriklerini her 10 saniyede bir gönderir, bağlandıktan 30 saniye sonra ve ondan sonra her 10 dakikada bir Teşhis'i çalıştırır ve 10 günden az kaldığında sertifikasını (yeni bir anahtarla) yeniler.

Ajanın bir ana bilgisayarda neleri değiştirdiğinin tam listesi, [Gereksinimler](requirements.md) sayfasındaki “Ajanın bir düğümde neleri değiştirdiği” bölümündedir.

## 4. Düğüm çevrimiçi olur

Pencere kendiliğinden yenilenir. `install` çalıştıktan birkaç saniye sonra 3. adım **Bağlandı** olur ve düğümün olaylarında ajan sürümüyle birlikte “ilk kez bağlandı” görünür. [İlk kullanıcılar](first-users.md) sayfasıyla devam etmek için **de1 düğümüne profil ekle** düğmesine basın: düğümde bir profil çalışana kadar düğüm “profil yok: kullanıcılar bu düğümü almaz” der.

| Durum | Anlamı |
|:--|:--|
| Kurulum bekliyor | Düğüm oluşturuldu, henüz hiçbir ajan kayıt olmadı. Düğümün sayfası komutun ne zamana kadar çalıştığını gösterir. |
| Çalışıyor | Ajan bağlı ve sunucuları çalışıyor. |
| Sağlayıcı kesintisi | Bağlantı 10 dakikadan kısa süre önce koptu; çoğunlukla sağlayıcıdan kaynaklanır. |
| Erişilemiyor | 10 dakikadır temas yok. Düğümün sayfası ajanı yeniden başlatma ve günlüğünü okuma komutunu gösterir. |
| Kullanımdan kaldırıldı | Filodan çıkarıldı. |

Düğüm çevrimiçi olmazsa ajanın günlüğünü sunucuda okuyun:

```sh
systemctl restart mistgate-node && journalctl -u mistgate-node -n 50 --no-pager
```

`panel connection lost` içeren satırlar nedeni taşır. Sunucu saatinin doğru olduğunu da kontrol edin.

## Yeni kurulum komutu

Kurulum bekleyen bir düğümün sayfasında, Genel bakış'ta ve ilk adımlar kontrol listesinde **Yeni kurulum komutu** düğmesi vardır. Komutun süresi dolduysa ya da kaybolduysa kullanın. Yeni bir komut, kullanılmamış eski komutların çalışmasını durdurur.

Daha önce kaydolmuş bir düğümün (kaybolan sertifika, yeniden kurulan sunucu, kaybolan durum dizini) **Sunucu** sekmesinde de aynı düğme bulunur; kullanımdan kaldırılmamış her düğümde vardır. Yalnızca sahip onu görür. Düğüm adını, profillerini ve kullanıcılarını korur; yeni ajan bağlandığında eski sertifika çalışmayı bırakır. Eski durum dizini hâlâ sunucudaysa `enroll` şu hatayla reddeder: `already enrolled; use --force to replace the identity`; `enroll` komutundan sonra `--force` ekleyin.

## Düğümü kaldırma

Düğümü açın, **Sunucu → Tehlikeli bölge → Kullanımdan kaldır** yolunu izleyin ve onaylamak için düğümün adını yazın. Panel ardından:

- düğümü her kullanıcının erişiminden çıkarır: kullanıcılar onu aboneliklerinde almaz, bağlantıları aynı kalır;
- düğümün sertifikasını iptal eder ve kullanılmamış kurulum komutlarını geçersiz kılar;
- düğümün kaydını ve geçmişini panelde ve denetim günlüğünde tutar; adı sonraki bir kurulum için serbest kalır;
- SSH ile kurulmuş bir düğümün kayıtlı SSH erişimini korur: kullanımdan kaldırılmış düğümün **Sunucu → SSH erişimi** bölümünde parolayı yine gösterebilirsiniz ve onu yalnızca **Kayıtlı erişimi unut** siler. Parolayı panel ürettiyse (MCP üzerinden bir döndürme), kullanımdan kaldırma penceresi sizi uyarır: parolayı gösterin ve bir kopyasını saklayın.

Ajan bağlıysa kullanımdan kalkma emrini alır ve sunucuda:

1. sunucularını durdurur;
2. düğümde WARP varsa onu kaldırır;
3. ana bilgisayar değişikliklerini geri alır: nftables tabloları, etiketli UFW kuralları (UDP dinleyici kuralları ve bir SSH kurulumunun 80/443 kuralları; SSH kuralı kalır), AmneziaWG arayüzleri, sysctl ve journald dosyaları (journald yeniden başlatılır; böylece boyut sınırı hemen kalkar) ve uygulandıysa Teşhis'in çözümleyici düzeltmesi;
4. durum dizinini siler (önce anahtarı) ve çıkar.

Sunucuda kalanlar: ikili dosya, birim (hâlâ etkin), çalışan çekirdekte zaten ayarlanmış değerler (fq, BBR, UDP tamponları, IP yönlendirme) bir sonraki yeniden başlatmaya kadar ve kurduysanız AmneziaWG çekirdek modülü ile paketleri. Gerisini elle kaldırın:

```sh
systemctl disable --now mistgate-node
rm -f /etc/systemd/system/mistgate-node.service
systemctl daemon-reload
rm -f /usr/local/bin/mistgate-node /usr/local/bin/mistgate-node.prev /usr/local/bin/mistgate-node.new /root/mistgate-node
rm -rf /var/lib/mistgate-node
```

> **Uyarı:** kullanımdan kaldırdığınızda çevrimdışı olan bir düğüm emri hiç almamıştır. Panel sertifikasını reddeder, ama ajan uyguladığı son durumu çalıştırmayı sürdürür (sunucuları sahip oldukları kullanıcılar için çalışmaya devam eder) ve bağlanmayı denemeyi bırakmaz. Panel bunu hemen söyler (“{name} düğümü komutu almadı”) ve kopyalanacak tüm komutları gösterir. Böyle bir sunucuyu elle temizleyin; aşağıdaki adımlarla başlayın, sonra yukarıdaki komutları çalıştırın.

```sh
systemctl disable --now mistgate-node      # its stop runs cleanup-net: tunnel interfaces, WARP routes, their tables
nft delete table inet mistgate_node        # the SSH guard and port-hopping rules
rm -f /etc/sysctl.d/90-mistgate.conf /etc/systemd/journald.conf.d/90-mistgate.conf
systemctl restart systemd-journald
```

O düğümde Teşhis'in çözümleyici düzeltmesini uyguladıysanız `/etc/systemd/resolved.conf.d/90-mistgate.conf` dosyasını kaldırıp systemd-resolved'u yeniden başlatın ya da `/etc/resolv.conf` dosyasını `/etc/resolv.conf.mistgate.bak` dosyasından geri koyun (`/etc/resolv.conf.mistgate.bak.link` varsa özgün dosya, içinde yazılı yola giden bir sembolik bağlantıydı).

`mistgate-node cleanup-net` her zaman çalıştırılabilir. AmneziaWG ve WARP arayüzlerini, WARP rotalarını ve kurallarını ve nftables tablolarını kaldırır; ajan başlarken bunları yeniden oluşturur. Ajanın sunucuya eklediği IPv6 adreslerine (düğümün kendi adresleri, durum dizinindeki `ipv6-owned.json` içinde adı geçer) dokunmaz; böylece bir yeniden başlatma ya da güncelleme onları düşürmez. `mistgate-node cleanup-net --ipv6-addresses` onları da kaldırır, ama ana bilgisayarın zaten sahip olduğu bir adresi asla. Bir düğümü kullanımdan kaldırmak onları da kaldırır.

## Kayıt başarısız olduğunda

| Gördüğünüz | Neden | Ne yapmalı |
|:--|:--|:--|
| `enroll: --panel, --sni, --ca-sha256 and --token are required` | Komut yapıştırılırken kesilmiş. | **Komutu kopyala** ile yeniden kopyalayın. |
| `--ca-sha256 must be 64 hex digits` | Aynısı. | Aynısı. |
| `enrollment token unknown, expired or used` | Komut bir saatten eski, zaten kullanılmış ya da bu düğüm için daha yenisi oluşturulmuş. | Düğümün sayfasında **Yeni kurulum komutu**. |
| `too many failed attempts, try later` | Bu adresten bir dakika içinde on başarısız deneme. | Bir dakika bekleyin ve yeni bir komut kullanın. |
| `node retired` | Düğüm kullanımdan kaldırıldı. | Yeni bir düğüm ekleyin. |
| `no certificate in the chain matches the pinned CA fingerprint`, `panel did not present its CA certificate` ya da `the CA returned by the panel does not match --ca-sha256` | `--panel` adresinde TLS'ye panel dışında bir şey yanıt veriyor: önündeki bir CDN ya da ters proxy, yanlış bir adres ya da komut oluşturulduktan sonra veri dizini yeniden oluşturulmuş bir panel. | Panelin DNS'ini doğrudan panel sunucusuna yönlendirin, `--public-url` ve `--agent-addr` değerlerini kontrol edin, yeni bir komut oluşturun. |
| Zaman aşımı ya da `connection refused` | Panel kapalı, bir güvenlik duvarı panele giden TCP 443'ü engelliyor ya da komuttaki adres yanlış. | Paneli ve güvenlik duvarını kontrol edin; düğümden `curl -I https://panel.example.com/` ile deneyin. |
| `already enrolled; use --force to replace the identity` | Durum dizininde zaten bir kimlik var. | `enroll` komutundan sonra `--force` ekleyin ya da önce `/var/lib/mistgate-node` dizinini silin. |
| `install: must run as root` | Root değilsiniz. | Komutu root olarak çalıştırın. |
| `holds no identity: run mistgate-node enroll first` | `enroll` başarısız oldu ya da `install` başka bir `--state-dir` aldı. | `enroll` komutunu aynı durum dizini ile yeniden çalıştırın. |
| Yönetim paneli **Kurulum komutu oluştur** işlemine “panel address is not configured” yanıtını veriyor | Panel ajanlar için hiçbir adres bilmiyor: setup `--public-url` olmadan çalıştı ve `serve` için `--agent-addr` yok. | Paneli `--agent-addr panel.example.com:443` ile yeniden başlatın. |

Daha fazla belirti ve çözüm [Sorun giderme](../operations/troubleshooting.md) sayfasında. Günlük düğüm ayarları [Düğümler](../guide/nodes.md) sayfasında.
