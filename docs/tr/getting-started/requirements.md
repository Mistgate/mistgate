---
title: Gereksinimler
description: Panel sunucusunun, düğüm sunucularının ve derleme makinesinin nelere ihtiyaç duyduğu ve ajanın bir düğümde neleri değiştirdiği.
---

Mistgate için panel adına bir Linux sunucusu, düğümler için bir ya da daha fazla Linux sunucusu ve bir alan adı gerekir. İkili dosyalar bir GitHub sürümünden gelir ya da kendi makinenizde kaynaktan derlersiniz.

## Panel sunucusu

| Ne | Gereksinim |
|:--|:--|
| Sistem | amd64 ya da arm64 üzerinde Linux. `mistgate serve` çalışır durması için systemd (örnek bir birim [Paneli kurma](install-panel.md) sayfasında). |
| Erişim | Kurulum için root. Çalışan panelin yalnızca TCP 443 ve 80'e bağlanabilmesi ve veri dizinine yazabilmesi gerekir. |
| Alan adı | A (IPv6 varsa AAAA) kaydı sunucuyu gösteren bir ana bilgisayar adı, örneğin `panel.example.com`. |
| Gelen portlar | TCP 443: kamuflaj sitesi, abonelikler, kullanıcı sayfaları, yönetim paneli (yol öneki ve ana bilgisayar kiplerinde) ve düğüm ajanları. Panel sertifikasını Let's Encrypt'ten alıyorsa TCP 80. Ayrı bir yönetim ya da ajan dinleyicisi seçmedikçe başka hiçbir şey. |
| Giden | Kullanıyorsanız HTTPS üzerinden Let's Encrypt. Düğümlerdeki sunucularınızın portları: panel her sunucuyu bir istemci gibi kontrol eder (UDP üzerinden Hysteria2, UDP üzerinden AmneziaWG). Yönetim panelinden düğüm olarak kuracağınız bir sunucuya SSH (TCP 22 ya da verdiğiniz port). Sürümler için HTTPS üzerinden GitHub (`api.github.com`, `github.com` ve indirme sunucusu): panel yeni imzalı düğüm paketini her 10 dakikada, panel güncellemesini de her 10 dakikada bir arar. WARP hesapları kaydederken, Turnstile captcha'yı açarken ya da R2'ye yedekleme yaparken HTTPS üzerinden Cloudflare. |
| Depolama | Veri dizini; varsayılan olarak `/var/lib/mistgate`: bir SQLite veritabanı, ana anahtar, düğüm paketi ve birkaç küçük dosya. |
| Diğer yazılım | Yok. Panel tek bir statik ikili dosyadır. Yalnızca kendi kendini güncellemesi systemd'yi (`systemctl`, `systemd-run`) çağırır. |

> **Not:** Cloudflare sürümü (bir önizleme) panel sunucusuna ihtiyaç duymaz; kendi gereksinimleri olan Workers Paid ve Cloudflare DNS'inde bir alan adı [Cloudflare sürümünü kurma](install-edge.md) sayfasındadır.

> **İpucu:** paneli kendi sunucusunda tutun. Aynı ana bilgisayardaki bir düğüm, TCP 443 (Hysteria2 orada varsayılan olarak bir HTTPS sitesi çalıştırır) ve TCP 80 için panelle yarışırdı.

## Düğüm sunucuları

| Ne | Gereksinim |
|:--|:--|
| Sistem | systemd'li Linux: Ubuntu 22.04 veya daha yenisi, ya da Debian 12 veya daha yenisi. amd64 ya da arm64. |
| Erişim | SSH yükleyicisi için: SSH ile root ya da etkileşimsiz `sudo -n` yapabilen bir SSH kullanıcısı, parolayla giriş; SSH portuna panelin sunucusundan ulaşılabilmelidir. Yükleyici sistem değişikliklerini root olarak yapar, sunucuda en az 256 MB belleğe ve panelde imzalı bir düğüm paketine ihtiyaç duyar; ayrıca zaten etkin olan bir UFW ya da firewalld'de SSH portunu, TCP 80'i, TCP 443'ü ve UDP 443'ü açar. Elle yapılan `mistgate-node install` de root olarak çalışır; ajan, daraltılmış bir yetki kümesiyle root olarak çalışır. |
| Adres | Herkese açık bir IPv4 adresi. İstemciler düğümün adresine bağlanır: sunucuyu gösteren bir alan adı ya da IP'nin kendisi. |
| nftables | `nft` komutu (Debian 12 ve Ubuntu 22.04+ üzerinde varsayılan olarak kuruludur). Ajan güvenlik duvarı kurallarını onunla yönetir. Port atlama, `inet` ailesinde `redirect` gerektirir; 5.2 ve sonrası çekirdeklerde vardır. |
| TUN aygıtı | Kullanıcı alanındaki AmneziaWG arka ucu ve WARP için `/dev/net/tun`. Konteyner tabanlı bir VPS'te (OpenVZ, LXC) TUN'u sağlayıcının kontrol panelinden etkinleştirin. |
| Saat | NTP ile eşitlenmiş. Ajan, saati panelinkinden 30 saniyeden fazla farklı olan bir saati bildirir; Teşhis'in `time_sync` kontrolü de ona bakar. |
| Gelen portlar | Düğümdeki her Hysteria2 ve AmneziaWG sunucusunun UDP portu: Hysteria2 443'ten başlar, bir AmneziaWG profili 10000 ile 60000 arasında rastgele bir port alır. Her VLESS sunucusunun TCP portu (varsayılan 443; 80 olmaz). Açtığınızda bir Hysteria2 profilinin port atlama aralığı (1024'ten başlayarak, en çok 20000 port). Hysteria2'nin istemci olmayan herkese gösterdiği HTTPS sitesi ve Let's Encrypt için TCP 443. Başka hiçbir şey tutmuyorsa Let's Encrypt için TCP 80. |
| Giden | Panele TCP (genellikle 443). Her VLESS sunucusunun REALITY hedefine TCP (genellikle 443): düğüm, istemci olmayan her ziyaretçi için ona bağlanır. Let's Encrypt, DNS ve düğüm WARP kullanıyorsa Cloudflare'in WARP uç noktaları. Yalnızca sahip düğümün ağ bant genişliğini ölçerken ve yeni bir düğüm için bir kez olmak üzere `www.speedtest.net` ve en yakın Ookla hız sunucularına HTTPS (8080 portu) ya da yedek olarak `speed.cloudflare.com`, `proof.ovh.net` ve `cachefly.net`: bkz. [Düğümler](../guide/nodes.md). |
| Yönetim portu yok | Ajan panele bağlanır. Düğümdeki hiçbir şeyin panelden bağlantı kabul etmesi gerekmez. |

![Bir düğümün portları: Hysteria2, atlama aralığı ve AmneziaWG için gelen UDP, HTTPS sitesi için TCP 443, Let's Encrypt için TCP 80 ve hız sınırlı SSH; giden tarafta ajanın panele bağlantısı, Let's Encrypt ve DNS, WARP ve hız testi; yönetim portu yok](../diagrams/node-ports.svg)

Bilmekte yarar var:

- **IPv6 isteğe bağlıdır.** IPv6'sı olmayan düğüm IPv4 üzerinden hizmet verir. Genel bir IPv6 adresi olan ama IPv6 üzerinden dışarı bağlanamayan düğüm, hiç IPv6'sı olmayandan daha kötüdür: AAAA yanıtı alan uygulamalar takılır. Teşhis'in `ipv6` kontrolü bu konuda uyarır.
- **Konteynerler.** OpenVZ ya da LXC üzerinde ajan sysctl değerlerini ayarlayamaz (bunu günlüğe yazar ve devam eder) ve AmneziaWG çekirdek modülü kurulamaz. TUN etkinse kullanıcı alanı arka ucu çalışır.
- **AmneziaWG çekirdek modülü.** İsteğe bağlı. AmneziaWG varsayılan olarak kullanıcı alanında çalışır ve modül gerektirmez. İstenirse ajan (ya da `mistgate-node awg prepare-kernel`) modülü Ubuntu'da Amnezia PPA'sından, Debian'da kaynaktan derler. Bunun için apt, systemd, konteynersiz ortam ve Secure Boot'un kapalı olması gerekir.
- **Temiz bir ana bilgisayar en iyisidir.** Teşhis, yolu kesebilecek şeyleri bildirir: diğer VPN panelleri ve konteynerleri, Xray, eski WireGuard arayüzleri, Mistgate'e ait olmayan nftables NAT kuralları ve Docker'ın FORWARD drop ilkesi (AmneziaWG yönlendirmesini bozar).

## Sürüm ikili dosyaları

Her [GitHub sürümü](https://github.com/Mistgate/mistgate/releases/latest), iki programın amd64 ve arm64 için statik Linux ikili dosyalarını (`mistgate-linux-amd64`, `mistgate-linux-arm64`, `mistgate-node-linux-amd64`, `mistgate-node-linux-arm64`), `SHA256SUMS` dosyasını ve panelin ile düğümlerin güncellendiği imzalı manifestleri taşır. İkili dosyalar projenin sürüm anahtarını taşır; bu yüzden onlardan kurulan bir panel GitHub'dan imzalı düğüm paketlerini alır ve bir systemd kurulumunda kendini güncelleyebilir ([Güncellemeler](../operations/updates.md) sayfasına bakın). O durumda bir derleme makinesine gerek yoktur.

## Kaynaktan derleme

| Araç | Sürüm |
|:--|:--|
| Go | 1.27. `go.mod` araç zincirini belirtir (`go1.27.4`); `GOTOOLCHAIN=auto` ile (varsayılan) daha eski bir Go onu indirir. |
| Node.js | 22 (CI'ın derlediği sürüm) ya da daha yenisi: yönetim paneli SPA'sı derlenir ve panel ikili dosyasına gömülür. |
| pnpm | 10: `web/package.json` `pnpm@10.33.2` sürümünü sabitler; Corepack açıksa onu kullanır. |
| make ve bir POSIX kabuğu | Windows'ta Git Bash ya da WSL. |
| git | `make build` sürümü (`git describe`) ve commit zamanını (`git log`) ikili dosyalara damgalar. git olmazsa sürüm `0.0.0-dev`, derleme zamanı 0 olur. |
| İnternet erişimi | Go modülleri ve npm paketleri için. |

`make build`, her iki mimari için statik Linux ikili dosyaları (`CGO_ENABLED=0`) üretir:

```sh
make build
# bin/mistgate-linux-amd64       bin/mistgate-node-linux-amd64
# bin/mistgate-linux-arm64       bin/mistgate-node-linux-arm64
```

> **Uyarı:** kendi derlediğiniz ikili dosyalar kendilerini yalnızca kendi sürüm anahtarınızla derlendiyse (`RELEASE_KEY=<public key> make build`) ve yalnızca sizin imzaladığınız paketlerden güncelleyebilir. Anahtarsız bir derlemeden kurulan düğümün bir kez elle güncellenmesi gerekir. İmzalı güncellemeler kullanmayı düşünüyorsanız anahtarı ilk derlemeden önce oluşturun: bkz. [Sürümler ve imzalama](../operations/releases.md).

## DNS ve sertifikalar

- **Panel.** Panelin ana bilgisayar adını, önüne bir CDN ya da proxy koymadan doğrudan sunucuya yönlendirin. Düğüm ajanları panelin kendi CA'sını sabitler ve Let's Encrypt doğrulamayı sunucunun kendisinde yapar; TLS'yi sonlandıran bir proxy üzerinden ikisi de çalışmaz.
- **Panel sertifikası.** Ya panelin onu Let's Encrypt'ten almasına izin verin (`serve --acme-domain`) ya da ona kendi sertifika dosyalarınızı verin (`serve --tls-cert` ve `--tls-key`). Bkz. [Paneli kurma](install-panel.md).
- **Gizli yönetim ana bilgisayarı.** Yönetim paneli gizli bir ana bilgisayar adındaysa o adın kendi DNS kaydı olmalı ve bir sertifikayla kapsanmalıdır. Let's Encrypt sertifikası adlarını herkese açık Certificate Transparency günlüklerinde yayımlar; kendi joker (wildcard) sertifikanız gizli adı onlardan uzak tutar.
- **Ajan uç noktası.** DNS kaydına ve herkese açık bir sertifikaya ihtiyaç duymaz. Ajanlar ona panelin adresinden gizli bir TLS adıyla (panelin alan adı altında rastgele bir etiket) ulaşır ve panel bu ada kendi CA'sından bir sertifikayla yanıt verir.
- **Düğümler.** Let's Encrypt sertifikalı bir Hysteria2 sunucusu için A kaydı düğümü gösteren bir alan adı gerekir: düğümün adresi ya da profilin Alan adı (SNI). Yalnızca IP'siyle bilinen bir düğüm bunun yerine kendinden imzalı (sabitlenmiş) sertifikayı kullanır. AmneziaWG sertifika gerektirmez.

## Ajanın bir düğümde neleri değiştirdiği

Ajan yalnızca burada listelenenleri ve yalnızca kendi nesnelerini değiştirir: başka bir programın nftables tablolarını asla düzenlemez, 1024'ün altındaki bir portu asla yönlendirmez ve bir port atlama aralığının sshd portlarını kapsamasına asla izin vermez.

| Ne | Nerede | Ne zaman |
|:--|:--|:--|
| Ajan ikili dosyası | `/usr/local/bin/mistgate-node` (`install --bin` yolu değiştirir). Kendi kendini güncelleme yanına `<binary>.new` ve `<binary>.prev` koyar. | `mistgate-node install`, kendi kendini güncelleme |
| Hizmet | `/etc/systemd/system/mistgate-node.service`, etkinleştirilir ve başlatılır. | `mistgate-node install` |
| Durum dizini | `/var/lib/mistgate-node` (kip 0700, dosyalar 0600): düğümün anahtarı ve sertifikası, panel CA'sı, panel adresi, son uygulanan durum, sunucularının sertifikaları, güncelleme işaretleri. | `enroll`, ardından ajan |
| sysctl | `/etc/sysctl.d/90-mistgate.conf`: `net.core.default_qdisc = fq`, `net.ipv4.tcp_congestion_control = bbr` ve en az 16 MiB olan `net.core.rmem_max` / `net.core.wmem_max`; çalışan çekirdeğe de uygulanır. Daha büyük UDP soket tamponları QUIC'in yük altında paket kaybetmemesine yardım eder. Yeni UDP aktarma soketleri `net.core.rmem_default` ve `net.core.wmem_default` değerlerinden en az 1 MiB miras alır; toplam UDP belleğini yine `net.ipv4.udp_mem` sınırlar. `net.netfilter.nf_conntrack_max` en az 65.536, `net.netfilter.nf_conntrack_buckets` en az 16.384 olur; daha yüksek canlı değerler korunur. | her ajan başlangıcında |
| journald | `/etc/systemd/journald.conf.d/90-mistgate.conf`: `SystemMaxUse=200M`, `RuntimeMaxUse=200M`. Dosya değiştiğinde journald yeniden başlatılır. | her ajan başlangıcında |
| Güvenlik duvarı: SSH koruması ve port atlama | nftables tablosu `inet mistgate_node`. | her ajan başlangıcında; port atlama aralıkları değiştiğinde |
| AmneziaWG | `mgawg<port>` arayüzleri, nftables tablosu `inet mistgate_awg` (istemciler için NAT, MSS sıkıştırma, istemciler arasında trafik yok, tünel üzerinden düğümde tünel adresine ping dışında hiçbir şeye erişilemez). IPv4 yönlendirmesi açık; bir tünelde IPv6 varsa IPv6 yönlendirmesi de açık, ama önce `accept_ra=1` olan arayüzler `accept_ra=2`'ye taşınır ki ana bilgisayar kendi IPv6'sını korusun. Yönlendirme değerleri yalnızca canlı olarak ayarlanır. | düğüm bir AmneziaWG sunucusu çalıştırdığı sürece |
| WARP | `mgwarp` arayüzü, 90 ve 110 tercihlerinde kurallarla 51820 yönlendirme tablosu, nftables tablosu `inet mistgate_warp`. | düğümde WARP olduğu sürece |
| UDP iletim kontrolü | Geçici nftables tablosu `inet mistgate_udpcheck`, prerouting önceliği -500'de yalnızca kontrolün 8 baytlık etiketini taşıyan datagramları sayar ve düşürür; kontrolden ya da bekleme süresi dolduktan sonra kaldırılır. | bir iletim kontrolü çalışırken |
| Torrent koruması | nftables tablosu `inet mistgate_torrentguard`: AmneziaWG istemcilerinin başlattığı akışların ilk paketlerini ajana verir ve ajanın BitTorrent olarak tanıdığı bir bağlantıyı çekirdekte düşürür. Ajan yanıt vermezse hiçbir şey bozulmaz (kuyruk atlanır). Hysteria2 kendi motorunun içinde kontrol edilir, ana bilgisayarda hiçbir şey olmaz. | **Tanınan BitTorrent trafiğini engelle** açıkken ve bir AmneziaWG sunucusu çalışırken |
| Ana bilgisayar güvenlik duvarı (UFW) | Her sunucunun UDP portu ve bir Hysteria2 sunucusunun atlama aralığı için `mistgate-node-managed-udp-v1-<port>` yorumuyla, her VLESS sunucusunun TCP portu için `mistgate-node-managed-tcp-v1-<port>` yorumuyla etiketlenmiş izin kuralları. Ajan, aynı port için kendisine ait olmayan bir kuralın üzerine asla kural eklemez ve etiketsiz hiçbir kuralı asla kaldırmaz. `ufw`, ajanın sanal alanı dışında geçici bir systemd birimi olarak çalışır. firewalld yalnızca kontrol edilir, asla değiştirilmez. | UFW etkin olduğu sürece |
| Çözümleyici | `/etc/systemd/resolved.conf.d/90-mistgate.conf` (systemd-resolved ile) ya da yeniden yazılmış bir `/etc/resolv.conf`; özgün dosya `/etc/resolv.conf.mistgate.bak` olarak saklanır. | yalnızca Teşhis'in çözümleyici düzeltmesini uyguladığınızda |
| Günlük boyutu | `journalctl --vacuum-size=200M --vacuum-time=7d`. | yalnızca Teşhis'in düzeltmesini uyguladığınızda |
| AmneziaWG çekirdek modülü | apt paketleri, Ubuntu'da Amnezia PPA'sı ya da Debian'da kaynaktan derleme, `/etc/modules-load.d/amneziawg.conf`. Geçici `mistgate-awg-prepare` birimi olarak çalışır. | yalnızca siz istediğinizde |

> **Not:** SSH koruması, sshd portlarına **yeni** bağlantıları kaynak adres başına (IPv6'da /64 başına) hız sınırlamasına tabi tutar: 10'luk bir patlama, sonra dakikada 6. Açık oturumlara asla dokunulmaz, kimse yasaklanmaz, loopback muaftır ve tablosu dolduğunda koruma sizi dışarıda bırakmak yerine eşleştirmeyi bırakır. Aynı adresten aynı anda çok sayıda SSH bağlantısı açan araçlar yine de bunu hissedebilir. Ajan sshd portlarını `sshd -T`, sshd yapılandırma dosyaları ve `ssh.socket` ile bulur; hiçbir şey bulamazsa 22'yi kullanır.

### Hizmet birimi

`mistgate-node install` birimi sunucunun belleğine göre boyutlandırarak kendisi yazar. Başlıca ayarları:

| Ayar | Değer |
|:--|:--|
| Başlatma | `mistgate-node run --state-dir /var/lib/mistgate-node` |
| Başlamadan önce | bir çökme döngüsü koruması: art arda üç kez çöken bir güncellemeden sonra önceki ikili dosyayı geri koyar |
| Her durmadan sonra | `mistgate-node cleanup-net`: AmneziaWG ve WARP arayüzlerini, WARP rotalarını ve kurallarını ve nftables tablolarını kaldırır; böylece çöken bir ajan arkasında bozuk yönlendirme bırakmaz (ajan başlarken bunları geri koyar) |
| Yeniden başlatma | 5 sn sonra `Restart=on-failure`; çıkış kodu 78 (kayıtlı değil ya da kullanımdan kaldırılmış) yeniden başlatılmaz |
| Bellek | `GOMEMLIMIT` RAM'in yaklaşık %60'ı (en az 64 MiB), `MemoryHigh` yaklaşık %70, `MemoryMax` yaklaşık %85; `LimitNOFILE=65536` |
| Ayrıcalıklar | yalnızca `CAP_NET_ADMIN` ve `CAP_NET_BIND_SERVICE` ile root olarak çalışır, `NoNewPrivileges=yes` |
| Dosyalar | `ProtectSystem=strict`: yazılabilir olanlar yalnızca durum dizini, ikili dosyanın dizini, `/etc/sysctl.d` ve `/etc/systemd/journald.conf.d` |
| Aygıtlar | standart sahte aygıtlar dışında yalnızca `/dev/net/tun` |
| Diğer | `ProtectHome`, `PrivateTmp`, `SystemCallFilter=@system-service`, `UMask=0077` ve olağan systemd sanal alanının geri kalanı |

Bir düğümü kullanımdan kaldırmanın neleri sildiği ve bir ana bilgisayarın elle nasıl temizleneceği, [Düğüm ekleme](add-node.md) sayfasındaki “Düğümü kaldırma” bölümünde anlatılır.
