# NetClone

**Boot edilebilir USB → disk imajı al → ağdan boot eden bilgisayarlara multicast ile aynı anda kur.**

NetClone, bir bilgisayarın diskini imaj olarak alıp, aynı imajı PXE ile ağdan boot
eden bilgisayarlara **tek seferde (multicast)** yazan bir appliance'tır.
Kaç bilgisayarın bağlanacağını siz belirlersiniz; o sayıya ulaşıldığında aktarım
otomatik olarak başlar.

```
                    ┌──────────────────────────────┐
   USB'den boot     │  NetClone sunucu (USB)       │
   edilen kaynak    │  • web paneli :8080          │
   bilgisayar  ────▶│  • imaj alma (NCI formatı)   │
                    │  • DHCP + TFTP + PXE         │
                    │  • udp-sender (multicast)    │
                    └──────────────┬───────────────┘
                                   │  multicast (tek akış)
              ┌────────────────────┼────────────────────┐
              ▼                    ▼                    ▼
        ┌──────────┐         ┌──────────┐         ┌──────────┐
        │ istemci  │         │ istemci  │   ...   │ istemci  │
        │ PXE boot │         │ PXE boot │         │ PXE boot │
        │ diske yaz│         │ diske yaz│         │ diske yaz│
        └──────────┘         └──────────┘         └──────────┘
```

---

## Özellikler

- **Tek USB, sıfır kurulum**: Debian tabanlı canlı sistem, tarayıcıda açılan panel.
- **Akıllı imaj alma**: sadece dolu bloklar taşınır (boş alan sıkıştırılıp atlanır),
  zstd ile hızlı sıkıştırma, SHA-256 ile bütünlük doğrulaması.
- **PXE sunucusu dahil**: dnsmasq ile DHCP + TFTP + UEFI (GRUB) ve legacy BIOS (PXElinux).
  İki mod: kendi DHCP'si (izole ağ) veya mevcut DHCP'ye **proxyDHCP** olarak eklenme.
- **Gerçek multicast**: `udpcast` ile imaj ağa bir kez çıkar, N bilgisayar aynı anda alır.
- **Sayaç kapısı**: "beklenen bilgisayar sayısı" dolunca aktarım kendiliğinden başlar;
  isterseniz "Şimdi Başlat" ile erken başlatabilirsiniz.
- **İstemci tarafı doğrulama**: her istemci aldığı akışın SHA-256'sını doğrular,
  GPT yedek başlığını hedef diskin sonuna taşır, bölüm tablosunu yeniden okur.
- **Canlı izleme**: her istemcinin yüzdesi, hızı, hedef diski ve hata mesajı panelde.

## Gereksinimler

| Bileşen | Gereksinim |
|---|---|
| Sunucu (imaj alınacak makine) | x86_64, UEFI veya legacy BIOS, en az 4 GB RAM, USB 3.0 önerilir |
| İstemciler | x86_64, PXE (network boot) destekli, UEFI veya legacy BIOS |
| Ağ | Tüm makineler aynı L2 segmentinde (switch). **Multicast'in router'dan geçmediğini unutmayın.** |
| ISO derleme | Docker Desktop (Windows), GitHub Actions veya Debian/Ubuntu + Go 1.19+ |

> Sanal makinelerde de çalışır (VMware/VirtualBox: "EFI" + "PXE boot" açık olmalı,
> ağ adaptörü "host-only" veya "bridged").

## 1) ISO'yu derle

### Windows (Docker Desktop ile)

```powershell
cd netclone
.\build\build-iso.ps1
```

Çıktı: `out\netclone-<sürüm>-amd64.iso`

### Debian / Ubuntu

```bash
sudo apt install live-build debootstrap xorriso squashfs-tools isolinux \
     syslinux-common syslinux-efi grub-efi-amd64-bin grub-pc-bin mtools dosfstools
cd netclone
sudo bash build/build-iso.sh
```

### GitHub Actions (yerel Docker gerekmez)

`netclone/` klasörünü kendi GitHub deposu olarak push edin, sonra:

1. **Actions** → **Build NetClone ISO** → **Run workflow**.
2. Yaklaşık 20-40 dakika sonra **Artifacts** bölümünden
   `netclone-iso-<run>-<attempt>` arşivini indirin.
3. `v*` biçiminde bir etiket push edildiğinde (ör. `git tag v1.0.0`) derleme
   otomatik başlar.

İndirdiğiniz arşivde ISO ve `.sha256` dosyası bulunur. ISO yaklaşık 2 GB'dır;
private repolarda artifact depolama kotasına takılabilir. Public repolarda
Actions kullanımı ücretsizdir. Artifact'lar 14 gün saklanır.

> Workflow, live-build'in disk/loop aygıtı erişimi için Docker'ı
> `--privileged` ile çalıştırır. Bu yalnızca kendi kodunuzu derlediğiniz
> GitHub runner'ında kullanılmalıdır.

## 2) USB'ye yaz

**Windows (yönetici PowerShell):**

```powershell
.\build\make-usb.ps1 -Iso .\out\netclone-1.0.0-amd64.iso -DiskNumber 3
```

**Linux:**

```bash
sudo dd if=out/netclone-1.0.0-amd64.iso of=/dev/sdX bs=8M status=progress oflag=sync
```

Alternatif: Rufus / balenaEtcher (ISO kalıbı olarak yazın).

## 3) Kullanım

### Adım 1 — Sunucuyu USB'den boot et

Bilgisayar açılır, otomatik oturum açılır ve panel tam ekran gelir
(gerekirse başka bir bilgisayardan `http://<sunucu-ip>:8080` açın).

> İlk açılışta PXE için gerekli çekirdek/initramfs dosyaları hazırlanır; bu
> 1-2 dakika sürebilir. Panel hemen açılır, "Dağıt" sekmesindeki oturum
> başlatıldığında dosyalar hazır değilse hata mesajı görürsünüz.

### Adım 2 — İmaj al

1. **Kaynak disk** listesinden imajı alınacak diski seçin (NetClone USB'si listede kilitlidir).
2. **Hedef konum**: harici diski takıp **Bağla**'ya basın (veya hazır bağlı bir yolu seçin).
3. İmaj adı ve sıkıştırma seviyesini seçip **İmajı Al**'a basın.
4. İş bitince otomatik olarak *Dağıt* sekmesine geçer.

> İmaj dosyaları `<hedef>/netclone/<imaj-adı>/image.nci` altında oluşur.
> İmajı başka bir diske kopyalayarak saklayabilirsiniz.

### Adım 3 — Dağıt

1. İmajı seçin.
2. **Beklenen bilgisayar sayısını** girin (örn. 20).
3. Ağ arayüzünü seçin.
4. DHCP modunu seçin:
   - **Bu bilgisayar DHCP sunucusu olsun** → izole laboratuvar ağı (önerilen).
   - **proxyDHCP** → ağda çalışan bir DHCP sunucusu varsa; IP dağıtımı ona bırakılır.
5. **Dağıtımı Başlat**'a basın.

### Adım 4 — İstemcileri ağdan boot et

İstemcilerde `F12` / `F2` / `Esc` ile boot menüsünü açıp **Network / PXE / LAN** seçin.
Her istemci:

1. DHCP'den IP alır,
2. NetClone sunucusundan çekirdek + initramfs'i indirir,
3. hedef diski seçer ve panele **"bekliyor"** olarak düşer.

Panelde sayaç `N / N` olduğunda multicast otomatik başlar. Aktarım bitince
istemciler diski senkronize eder, bölüm tablosunu yeniler ve yeniden başlar.

### İptal / erken başlatma

- **Şimdi Başlat**: beklenen sayı dolmadan, bağlı istemcilerle başlatır.
- **İptal**: aktarımı durdurur, DHCP/TFTP kapanır, istemciler çıkar.
- **Oturumu Kapat**: biten oturumu temizler.

## 4) Disk seçimi politikası

| Politika | Davranış |
|---|---|
| `İlk dahili disk` (varsayılan) | Sabit diskler içinde isim sırasına göre ilk uygun disk (`/dev/sda`, `/dev/nvme0n1`…) |
| `İmaja sığan en küçük disk` | İmaj boyutuna yeten en küçük disk (karışık donanım için) |
| `En büyük disk` | En büyük uygun disk |

Takılı USB bellekler (removable) ancak başka disk yoksa hedef olur.
İmajdan küçük diskler otomatik atlanır ve istemci hata bildirir.

## Sınırlar ve uyarılar

- **Veri kaybı**: dağıtım, hedef diski tamamen siler. Önce test edin.
- **Multicast**: aynı VLAN/L2 segment gerekir; router, Wi-Fi ve VLAN geçişlerinde çalışmaz.
- **DHCP çakışması**: "DHCP sunucusu" modunda ağda ikinci bir DHCP olmamalı.
- **BitLocker / LUKS**: şifreli disklerin imajı şifreli hâliyle alınır; açmak için anahtar gerekir.
- **Windows/BIOS**: raw disk kopyası olduğu için önyükleme kayıtları korunur; hedef disk
  imajdan büyükse GPT yedek başlığı otomatik taşınır.
- Bu proje **gerçek donanımda henüz test edilmemiştir**; üretimde kullanmadan önce
  laboratuvarda doğrulayın (`docs/troubleshooting.md`).

## Dokümantasyon

- [`docs/architecture.md`](docs/architecture.md) — iç tasarım, NCI formatı, API
- [`docs/troubleshooting.md`](docs/troubleshooting.md) — sorun giderme
- [`docs/production.md`](docs/production.md) — üretim öncesi kontrol listesi

## Geliştirme

```bash
go build ./...            # linux hedefi icin: GOOS=linux go build ./...
go test ./internal/nci    # imaj formatı testleri
go vet ./...
```

Kod yapısı:

```
cmd/netclone-server   → web paneli + orkestrasyon
cmd/netclone-client   → netboot istemci ajanı
internal/nci          → NCI imaj formatı (yazıcı/okuyucu, SHA-256)
internal/capture      → disk → imaj akışı
internal/server       → HTTP API, oturum, PXE/multicast
internal/agent        → istemci iş akışı
internal/sysinfo      → disk / ağ / mount bilgisi
build/                → live-build ISO, netboot initramfs, USB araçları
.github/workflows/    → GitHub Actions ile ISO derleme
```

## Lisans

MIT
