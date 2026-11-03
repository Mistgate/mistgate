---
title: Paneli kurma
description: Mistgate'i indirin ya da derleyin, paneli mistgate setup ile hazırlayın, systemd altında çalıştırın ve ilk kez giriş yapın.
---

Bu sayfa, temiz bir Linux sunucusunu sahip hesabıyla çalışan bir panele götürür. Örneklerde `panel.example.com` kullanılır; önce [Gereksinimler](requirements.md) sayfasına bakın. Panel sunucusundaki komutlar root olarak çalıştırılır. Sunucuya SSH erişimi olan bir yapay zekâ ajanı aynı adımları sizin yerinize yapabilir: [Yapay zekâ ajanı kılavuzundaki](ai-agents.md) kurulum istemine bakın.

> **Not:** bu sayfa kendi sunucunuzdaki bir panel içindir. Paneli sunucu olmadan Cloudflare Workers üzerinde çalıştırmak için (önizleme) [Cloudflare sürümünü kurma](install-edge.md) sayfasına bakın.

## Tek satırda kurulum

amd64 ya da arm64 üzerinde, systemd'li temiz bir Linux sunucusunda şunu root olarak çalıştırın. Alan adı doğrudan sunucuyu göstermeli ve TCP 80 ile 443 portları boş olmalıdır. İmza doğrulaması için ya `ssh-keygen -Y verify` destekli `openssh-client` ya da OpenSSL 3.2 veya daha yenisi gerekir. Yükleyici son sürümü indirir, ikili dosyayı `SHA256SUMS` ve imzalı panel manifestiyle karşılaştırır, sonra setup'ı çalıştırır ve systemd hizmetini kurar. Güvenlik duvarı kurallarına dokunmaz; UFW ya da firewalld etkinse eklenecek kuralları yazdırır.

```sh
curl -fsSL https://mistgate.app/install.sh | sudo bash -s -- --public-url https://panel.example.com
```

Yükleyici varsayılan olarak URL'nin ana bilgisayarı için bir Let's Encrypt sertifikası ister. Değişiklik yapmadan önce sorar; etkileşimsiz çalıştırmak için `--yes` ekleyin. Ana bilgisayarı değiştirmeden bir sürümü indirip doğrulamak için `--dry-run`, sabitlenmiş bir sürümü kurmak için `--version vX.Y.Z` kullanın. `--admin-host`, `--admin-listen`, `--acme-domain` ve `--data-dir`, aşağıda anlatılan setup ve hizmet seçeneklerini belirler.

Bittiğinde tek kullanımlık kurulum bağlantısını bir tarayıcıda açın ve sahip hesabını oluşturun. Bağlantı 30 dakika sonra geçerliliğini yitirir. Ardından **Düğümler → Düğüm ekle** bölümünden bir düğüm ekleyin.

## Elle kurulum

### 1. İkili dosyayı edinin

Panel, yönetim web uygulaması içine gömülü, amd64 ya da arm64 için tek bir statik Linux ikili dosyasıdır. Kendinizinkini derlemek için bir nedeniniz yoksa resmî sürümü kullanın.

#### GitHub Releases'tan (önerilen)

[GitHub Releases](https://github.com/Mistgate/mistgate/releases/latest) üzerindeki her sürüm şunları taşır: paneli (`mistgate-linux-amd64`, `mistgate-linux-arm64`), düğüm ajanını (`mistgate-node-linux-amd64`, `mistgate-node-linux-arm64`), `SHA256SUMS` ve `BUILDINFO` dosyalarını ve iki imzalı manifesti: düğüm ajanları için `manifest.sig` ile `manifest.json`, panel için `panel-manifest.sig` ve `panel-manifest.sshsig` ile `panel-manifest.json`. Resmî ikili dosyalar projenin sürüm anahtarını taşır; bu yüzden onlardan kurulan bir panel imzalı düğüm ajanlarını kendisi indirir ve sonraki sürümlerden kendini güncelleyebilir ([Güncellemeler](../operations/updates.md) sayfasına bakın).

Panel sunucusunda root olarak (`uname -m`, amd64 için `x86_64`, arm64 için `aarch64` yazdırır):

```sh
ARCH=amd64    # arm64 on an ARM server
cd /root
for f in "mistgate-linux-$ARCH" SHA256SUMS panel-manifest.json; do
  curl -fsSLO "https://github.com/Mistgate/mistgate/releases/latest/download/$f"
done
sha256sum --check --ignore-missing SHA256SUMS
grep -q "$(sha256sum "mistgate-linux-$ARCH" | cut -d' ' -f1)" panel-manifest.json && echo "listed in panel-manifest.json"
install -m 0755 "mistgate-linux-$ARCH" /usr/local/bin/mistgate
mistgate version
```

Bu kontrollerin neyi kanıtladığı ve neyi kanıtlamadığı:

- `sha256sum --check`, dosyanın bozulmadan geldiğini ve sürümle birlikte yayımlanan sağlama toplamlarıyla eşleştiğini kanıtlar. `SHA256SUMS` sürüm iş akışı tarafından yazılır ve imzalı değildir; bu yüzden ikili dosyayı kimin derlediğini kanıtlamaz.
- Aynı SHA-256, bakımcının her ikili dosyayı etiketten yeniden derledikten sonra sürüm anahtarıyla çevrimdışı imzaladığı `panel-manifest.json` içinde de listelenir. Tek satırlık yükleyici `ssh-keygen -Y verify` ile `panel-manifest.sshsig` dosyasını tercih eder; OpenSSL 3.2 veya daha yenisiyle `panel-manifest.sig` dosyasına döner. Bu elle yolda `grep` yalnızca bir tutarlılık kontrolüdür.
- Bundan sonra imzaları panelin kendisi kontrol eder: bir panel sürümünü kurar ve düğüm paketini ajanlara yalnızca imza, içine derlenmiş sürüm anahtarıyla doğrulanıyorsa verir. `mistgate version`, sürümü ve o anahtarın parmak izini yazdırır.

#### Kaynaktan

[Gereksinimler](requirements.md) sayfasında listelenen araçları taşıyan bir derleme makinesinde:

```sh
git clone https://github.com/Mistgate/mistgate.git
cd mistgate
make build
scp bin/mistgate-linux-amd64 root@panel.example.com:/root/
ssh root@panel.example.com 'install -m 0755 /root/mistgate-linux-amd64 /usr/local/bin/mistgate'
```

`bin/` düğüm ajanlarını da (`mistgate-node-linux-amd64`, `mistgate-node-linux-arm64`) barındırır; onları [elle düğüm kurulumu](add-node.md) için saklayın.

> **Uyarı:** `RELEASE_KEY` olmadan yapılan bir derlemenin sürüm anahtarı yoktur: düğüm ajanları kendilerini asla güncellemez, panel panel sürümlerini kuramaz ve SSH kurulumunun kuracağı güvenilir bir ajan paketi olmaz. Kendi anahtarınızla yapılan bir derleme yalnızca sizin imzaladığınız sürümlere güvenir. Anahtarı ilk derlemeden önce oluşturun: bkz. [Sürümler ve imzalama](../operations/releases.md).

### 2. Yönetim paneline nasıl ulaşılacağını seçin

Herkese açık adres herkese bir kamuflaj sitesi gösterir. Yönetim paneline üç yoldan biriyle ulaşılır; seçim `mistgate setup` çalıştırılırken bir kez yapılır:

| Kip | `setup` bayrakları | Yönetim paneli adresi | Ne zaman seçilir |
|:--|:--|:--|:--|
| Gizli yol öneki (varsayılan) | `--public-url https://panel.example.com` | `https://panel.example.com/<secret>/`; gizli kısım 24 rastgele karakterdir | En basiti: tek ana bilgisayar adı, tek sertifika. |
| Gizli ana bilgisayar | `--public-url https://panel.example.com --admin-host <secret host>` | `https://<secret host>/` | Yönetim panelinin kimsenin tahmin edemeyeceği bir ana bilgisayar adında olmasını istiyorsanız. Bunun için DNS kaydı ve onu kapsayan bir sertifika gerekir. |
| Ayrı dinleyici | `--public-url https://panel.example.com --admin-listen 127.0.0.1:8081` | `http://localhost:8081/`, bir SSH tüneli üzerinden | Yönetim paneli herkese açık portta hiç görünmez. |

- Yönetim paneliyle eşleşmeyen istekler (yanlış önek, bilinmeyen ana bilgisayar, bilinmeyen yol) kamuflaj sitesini alır; böylece bir yoklayıcı ucu ucuna kaçırmayı rastgele bir tahminden ayıramaz.
- `--public-url` her zaman verin. Her abonelik bağlantısının tabanıdır ve düğüm kurulum komutlarının taşıdığı adrestir. O olmadan panel abonelik bağlantısı vermez; `serve`'e `--agent-addr` verilmedikçe düğüm eklemek de başarısız olur.
- `--admin-host` ve `--admin-listen` birlikte kullanılamaz.
- Ayrı yönetim dinleyicisi düz HTTP konuşur. Onu loopback'e bağlayın ve SSH üzerinden ulaşın:

  ```sh
  ssh -N -L 8081:127.0.0.1:8081 root@panel.example.com
  ```

  Ardından tarayıcınızda `http://localhost:8081/` adresini açın. Kendi tarafınızda aynı portu kullanın: geçiş anahtarları, setup'ın sakladığı `http://localhost:<port>` adresine bağlıdır.
- `--rp-id` ve `--rp-origins`, setup'ın yönetim adresinden türettiği WebAuthn ayarlarını geçersiz kılar. Bunlara nadiren ihtiyaç duyarsınız.

> **Uyarı:** setup bu adresleri bir kez saklar. Yeniden çalıştırmak onları olduğu gibi bırakır ve Mistgate'te sonradan değiştirecek bir komut yoktur. Devam etmeden önce seçin; baştan başlamak yeni bir veri dizini ve her düğümü yeniden kaydetmek demektir.

### 3. Setup'ı çalıştırın

```sh
mistgate setup --public-url https://panel.example.com
```

Şunu yazdırır:

```text
Data dir:   /var/lib/mistgate
Admin URL:  https://panel.example.com/<secret>/
Setup link: https://panel.example.com/<secret>/setup#<token>
The link works once and expires in 30 minutes. Open it in a browser and create your admin (a passkey, or a password with an authenticator code).
```

Setup'ın yaptıkları:

1. `/var/lib/mistgate` veri dizinini 0700 kipiyle oluşturur (`--data-dir` yolu değiştirir).
2. `master.key` dosyasını oluşturur: 0600 kipinde 32 rastgele bayt. Panelin sakladığı her sırrı şifreler.
3. `mistgate.db` veritabanını oluşturur ve şemayı uygular.
4. Seçilen kipin adreslerini saklar ve iki sır daha üretir: düğüm ajanlarının panele ulaşmak için kullandığı TLS adı ve abonelik bağlantılarının gizli yol öneki.
5. 30 dakika geçerli, tek kullanımlık bir kurulum bağlantısı yazdırır.

Yönetim adresini kendinize saklayın. Setup'ı yeniden çalıştırmak güvenlidir: mevcut yapılandırmayı korur ve hiç yönetici yokken yeni bir kurulum bağlantısı yazdırır.

### 4. Sertifikayı seçin

| Seçenek | `serve` bayrakları | Ne olur |
|:--|:--|:--|
| Let's Encrypt | `--acme-domain panel.example.com` | Panel sertifikayı kendisi alır ve yeniler (herkese açık dinleyicide TLS-ALPN-01; bu yüzden 443 portundan ulaşılabilir olmalıdır). `:80` üzerindeki ikinci bir dinleyici HTTP-01'e yanıt verir ve düz HTTP'yi HTTPS'e yönlendirir (`--acme-http`; boş değer kapatır). Sertifikalar `<data-dir>/acme` içinde önbelleğe alınır. `--acme-email` bir iletişim adresi ekler. Kullanmak, Let's Encrypt hizmet şartlarını kabul etmek anlamına gelir. |
| Kendi sertifikanız | `--tls-cert /path/fullchain.pem --tls-key /path/privkey.pem` | Panel PEM dosyalarınızı sunar ve sertifika dosyası değiştiğinde yeniden okur (en çok 30 saniyede bir kontrol edilir); bu yüzden başka bir aracın yenilemesi yeniden başlatma gerektirmez. |
| İkisi birden | iki bayrak kümesi de | `--acme-domain` içinde listelenen adlar Let's Encrypt sertifikası alır; diğer her ad sizin sertifikanızı alır. Kendi joker (wildcard) sertifikanızın kapsadığı gizli bir yönetim ana bilgisayarı için kullanışlıdır. |

`--acme-domain` yalnızca düz ana bilgisayar adlarını kabul eder, joker karakter olmaz. Birkaç ad için tekrarlanabilir.

> **Not:** gizli bir yönetim ana bilgisayarı için Let's Encrypt sertifikası, o adı herkese açık Certificate Transparency günlüklerine koyar. Gizli kalması için onu kendi joker (wildcard) sertifikanızla kapsayın.

### 5. Kamuflaj sitesi

Başka hiçbir şey yapmazsanız panel yerleşik bir “Coming soon” sayfası gösterir. Her kurulum aynı sayfayı taşır, bu yüzden tanınabilir; kendi siteniz daha iyidir. Statik dosyaları bir dizine koyun ve `--decoy-dir` verin:

- `/` ve her dizin kendi `index.html` dosyasını sunar.
- `404.html` ve `429.html`, varsa, yerleşik “not found” ve “too many requests” sayfalarının yerini alır. Her bilinmeyen yol, yanlış yönetim öneki ve bilinmeyen abonelik bağlantısı aynı 404'ü alır.
- `robots.txt` yoksa, bilinen yapay zekâ tarayıcılarını geri çeviren yerleşik bir dosya kullanılır.
- Yalnızca GET ve HEAD sunulur. Adı noktayla başlayan dosya ve dizinler asla sunulmaz ve yanıtlar Last-Modified ya da ETag başlığı taşımaz.

Panel bu dizini yalnızca okur. Aşağıdaki systemd birimiyle onu `/home` ve `/root` dışında tutun (örneğin `/srv/mistgate-decoy`). [Kendi kamuflaj siteniz](../guide/decoy-site.md) sayfası iyi bir sitenin neye benzediğini anlatır ve bir yapay zekâ ajanına benzersiz bir site uyduran bir istem içerir.

### 6. Ön planda deneyin

```sh
mistgate serve --listen :443 --acme-domain panel.example.com
```

`--listen` varsayılan olarak `127.0.0.1:8080` olur; bu yüzden gerçek bir sunucuda `--listen :443` verin. Günlük stderr'e gider; herkese açık dinleyici için `listening` satırlarına (80 portu için `acme-http`) bakın. Çalıştığında Ctrl+C ile durdurun.

### 7. systemd altında çalıştırın

Bunu `/etc/systemd/system/mistgate.service` olarak kaydedin ve `ExecStart` satırını seçtiğiniz bayraklarla değiştirin:

```ini /etc/systemd/system/mistgate.service
[Unit]
Description=Mistgate panel
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=/usr/local/bin/mistgate serve --listen :443 --acme-domain panel.example.com
Restart=on-failure
RestartSec=5
TimeoutStopSec=30

# The panel writes only its data directory and only needs to bind ports 443 and 80.
NoNewPrivileges=yes
CapabilityBoundingSet=CAP_NET_BIND_SERVICE
ProtectSystem=strict
ReadWritePaths=/var/lib/mistgate
ProtectHome=yes
PrivateTmp=yes
PrivateDevices=yes
ProtectClock=yes
ProtectControlGroups=yes
ProtectKernelTunables=yes
ProtectKernelModules=yes
ProtectKernelLogs=yes
ProtectHostname=yes
LockPersonality=yes
RestrictRealtime=yes
RestrictSUIDSGID=yes
RestrictNamespaces=yes
RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX AF_NETLINK
SystemCallArchitectures=native
SystemCallFilter=@system-service
UMask=0077

[Install]
WantedBy=multi-user.target
```

```sh
systemctl daemon-reload
systemctl enable --now mistgate
journalctl -u mistgate -f
```

Neden böyle göründüğü:

- `mistgate setup` root olarak çalıştı; bu yüzden veri dizini root'a aittir ve hizmet de root olarak çalışır. Düşük portlara bağlanma dışındaki her yetki bırakılır ve yalnızca `/var/lib/mistgate` yazılabilir. Başka bir `--data-dir` kullanırsanız `ReadWritePaths` satırını değiştirin.
- Panel SIGTERM ile düzgün biçimde durur. `Restart=on-failure` ile systemd onu bir dinleyici başarısız olduğunda yeniden başlatır, siz durdurduğunuzda değil.
- Her bayrağın bir `MISTGATE_*` ortam değişkeni vardır; bayrakları `Environment=` satırlarına ya da bir `EnvironmentFile=` dosyasına taşıyabilirsiniz. Bkz. [Yapılandırma](../reference/configuration.md).
- `--decoy-dir`, `--tls-cert` ya da `--tls-key` ile dosyaların yalnızca okunabilir olması yeterlidir.

#### Ters proxy'nin arkasında

Başka bir web sunucusunun bu ana bilgisayarda 443 portuna sahip olması gerekiyorsa Mistgate onun arkasında çalışabilir:

- Herkese açık dinleyiciyi loopback üzerinde TLS olmadan çalıştırın (`--listen 127.0.0.1:8080`, `--acme-domain` ve `--tls-cert` yok) ve `panel.example.com` için TLS'yi proxy'nin sonlandırmasına, Host başlığını ve yolu değiştirmeden iletmesine izin verin.
- Hız sınırlarının, oturumların ve denetim günlüğünün gerçek istemci adresini `X-Forwarded-For` ya da `Forwarded` başlığından görmesi için `--trusted-proxy 127.0.0.1` ekleyin.
- Düğüm ajanları TLS'yi panelin kendisinden ister. Onlara kendi dinleyicilerini verin ve adresini kurulum komutlarına koyun: `--agent-listen :8443 --agent-addr panel.example.com:8443`. Proxy o porta dokunmamalıdır.

Ajan portu o zaman yalnızca gizli TLS adına yanıt veren ayrı bir porttur; paylaşılan 443 portunda ise kamuflaj sitesinin arkasına saklanır. Mümkünse yukarıdaki düz kurulumu tercih edin.

### 8. İlk kez giriş yapın

3. adımdaki kurulum bağlantısını bir tarayıcıda açın; `#` işaretinden sonraki kısım dahil bağlantının tamamını.

1. **Dil.** İngilizce, Rusça, Basitleştirilmiş Çince, Farsça ya da Türkçeyi seçin. Sonra değiştirebilirsiniz.
2. **Yöneticiyi oluştur.** KULLANICI ADI alanı (başlangıçta `admin` yazar) giriş adınız ve panelin gösterdiği addır. Sonra:
   - **Geçiş anahtarı oluştur**: tarayıcı parmak izi, yüz, PIN ya da güvenlik anahtarı ister. Anahtar cihazınızda kalır. Önerilen yol budur.
   - **parola + doğrulayıcı kodu**: en az 12 karakterli bir parola, ardından QR kodunu bir doğrulayıcı uygulamayla (Google Authenticator, 1Password, Aegis ve benzerleri) tarayın, 6 haneli kodu yazın ve **Yöneticiyi oluştur** düğmesine basın.
3. **Tamam.** **İlk düğümünü ekle** ya da **Sonra, önce bir göz atayım** seçeneğini seçin.

İlk yönetici sahiptir. Kurulum sihirbazı ayrıca ilk kullanıcılarınız için boş bir “Herkes” grubu oluşturur.

- Kullanıcı adı 3 ile 64 karakter arasındadır: a–z, 0–9 ve `. _ @ -`.
- Bağlantı yalnızca yönetici oluşturulduğunda harcanır. Süresi dolduysa ya da bir şey ters gittiyse yeni bir bağlantı için `mistgate setup` komutunu yeniden çalıştırın.
- Geçiş anahtarı yalnızca setup'ın sakladığı yönetim adresinde çalışır. “Tarayıcı bu adres için geçiş anahtarını reddetti” hatası, sayfanın başka bir adresle açıldığı anlamına gelir.

Sonraki girişlerde yönetim adresinde **Geçiş anahtarıyla giriş yap** ile ya da **başka bir yolla**: kullanıcı adı, parola ve kodla giriş yaparsınız. Bir saat içinde beş yanlış parola o kullanıcı adını 15 dakika kilitler. Telefonunuzu ya da geçiş anahtarınızı kaybettiyseniz: [Güvenlik](../operations/security.md) sayfası `mistgate auth reset-login` komutunu anlatır.

### 9. Hemen yedek alın

Panelin bildiği her şey veri dizinindedir:

| Dosya | Nedir |
|:--|:--|
| `mistgate.db`, `mistgate.db-wal`, `mistgate.db-shm` | Veritabanı: yöneticiler, düğümler, profiller, kullanıcılar, cihazlar, trafik, olaylar, denetim günlüğü. İçindeki sırlar ana anahtarla şifrelidir. |
| `master.key` | Saklanan sırları şifreleyen anahtar; her düğümün güvendiği panel CA'sının anahtarı da buna dahildir. |
| `acme/` | Let's Encrypt hesabı ve sertifikalar (yalnızca `--acme-domain` ile). |

Veritabanının tutarlı olması için panel durdurulmuşken şimdi bir kopya alın:

```sh
systemctl stop mistgate
tar czf mistgate-backup-$(date +%F).tgz -C /var/lib mistgate
systemctl start mistgate
```

> **Uyarı:** kopya ana anahtarı da içerir: ona sahip olan herkes panelin her sırrını okuyabilir. Onu şifreli tutun ve sunucunun dışında saklayın. Veri dizini olmazsa her düğümün yeniden kaydedilmesi gerekir ve her kullanıcı, bağlantı ve anahtar kaybolur.

Düzenli kopyalar için **Ayarlar → Yedekler** bölümünde [şifreli yedekleri](../operations/backups.md) açın: panel tutarlı bir anlık görüntüyü çevrimdışı kurtarma anahtarınıza şifreler ve zamanlanmış olarak Cloudflare R2 kovanıza yükler. Paneli kendi sürüm anahtarınızla derlediyseniz o anahtar dosyasını da çevrimdışı tutun ve yedekleyin.

## Sırada

[Düğüm ekleme](add-node.md): SSH ile otomatik olarak ya da kendi çalıştırdığınız bir komutla.
