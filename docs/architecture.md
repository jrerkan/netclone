# NetClone mimarisi

## Bileşenler

```
┌────────────────────────── NetClone sunucu (canlı USB) ──────────────────────────┐
│                                                                                 │
│  netclone-server (:8080)                                                        │
│   ├── web/            gömülü tek sayfa panel (vanilla JS)                       │
│   ├── capture         /dev/<disk> → zstd → image.nci (+manifest.json)           │
│   ├── session         oturum durum makinesi, istemci sayaçları                  │
│   ├── netboot         dnsmasq.conf üretimi, grub.cfg / pxelinux.cfg             │
│   └── multicast       udp-sender süreç yönetimi                                 │
│                                                                                 │
│  dnsmasq        DHCP (+ proxyDHCP) ve TFTP (/run/netclone/netboot)              │
│  udp-sender     imajı multicast ile yayınlar                                    │
└─────────────────────────────────────────────────────────────────────────────────┘
                                    ▲  HTTP (kayıt, plan, ilerleme)
                                    │  multicast UDP
┌──────────────────────────── netboot istemci ────────────────────────────────────┐
│  vmlinuz + initrd.img (özel initramfs)                                          │
│  /init: modül yükleme → DHCP → netclone-client                                  │
│  netclone-client: disk seçimi → kayıt → plan → udp-receiver | zstd -d | yazma    │
└─────────────────────────────────────────────────────────────────────────────────┘
```

## NCI imaj formatı

```
offset 0   ┌───────────────────────────────┐
           │ 128 bayt başlık              │  sihir "NCLONE01", sürüm, disk boyutu,
           │                               │  sektör, chunk, şema, kaynak adı
offset 128 ├───────────────────────────────┤
           │ zstd çerçevesi                │
           │  ┌─────────────────────────┐  │
           │  │ kayıt: data  (13+n bayt)│  │  offset, uzunluk, veri
           │  │ kayıt: zero  (13 bayt)  │  │  offset, sıfırlanacak uzunluk
           │  │ ...                     │  │
           │  │ kayıt: end   (13 bayt)  │  │
           │  │ kayıt: hash  (13+32)    │  │  akışın SHA-256'sı
           │  └─────────────────────────┘  │
           └───────────────────────────────┘
```

- **Sadece dolu bloklar** taşınır. 4 MiB'lik parçalar taranır, tamamen sıfır olan
  bölgeler `zero` kaydı olarak (13 bayt) geçer. Hedef diskte bu aralıklar
  `BLKZEROOUT` ioctl'i ile (desteklenmiyorsa yazarak) sıfırlanır — böylece eski
  veri kalıntısı kalmaz.
- **Hash**, `hash` kaydı hariç tüm kayıt baytları üzerinden hesaplanır; alıcı
  kayıtları yeniden serileştirip aynı özeti hesaplar. Aktarım sırasında oluşan
  herhangi bir bozulma tespit edilir.
- Sıkıştırma dış `zstd` süreciyle yapılır: sunucuda `zstd -1 -T<n> -c`, istemcide
  `zstd -d -c`. Go tarafında harici bağımlılık yoktur.

### Neden partclone değil?

Partclone dosya sistemine özeldir ve istemcide ilgili aracın bulunmasını gerektirir.
NCI, ham disk akışını seyrek (sparse) olarak taşıdığı için her dosya sistemiyle
(NTFS, ext4, XFS, Btrfs, LVM, şifreli bölümler) aynı şekilde çalışır; tek şart
hedef diskin imaj kadar büyük olmasıdır.

## Oturum durum makinesi

```
idle ──POST /api/session──▶ starting ──dnsmasq hazır──▶ waiting
                                                          │
                                     istemci sayısı == expected
                                                          ▼
                                                      sending ──udp-sender biter──▶ done
                                                          │
                                             iptal / hata ▼
                                                    aborted / failed
```

- `waiting`: dnsmasq (DHCP+TFTP) çalışıyor, `grub.cfg`/`pxelinux.cfg` bu oturumun
  token'ını ve sunucu adresini içeriyor.
- `sending`: istemcilere plan yanıtı `start` döner; 3 saniye sonra `udp-sender`
  `--min-receivers N --full-duplex --nopointopoint --nokbd` ile başlar.
- `done`: `udp-sender` çıkar, dnsmasq kapatılır. İstemciler sonuçlarını bildirir.

Geç katılan istemci (`late`) kabul edilmez; panelde "geç kaldı" olarak görünür.

## PXE akışı

1. İstemci DHCP isteği atar (option 60 = PXEClient).
2. dnsmasq, `option:client-arch` 7/9 (x86_64 UEFI) ise `grubnetx64.efi`,
   aksi hâlde `pxelinux.0` dosyasını boot dosyası olarak verir.
3. GRUB/PXELINUX TFTP ile `grub.cfg` / `pxelinux.cfg/default` okur.
4. Çekirdek parametreleri:

   ```
   netclone.server=<ip>:8080 netclone.token=<oturum-token> netclone.policy=<politika>
   ```

5. Çekirdek + özel initramfs yüklenir (~20 MB).
6. `/init` modülleri yükler (PCI/USB modalias taraması), DHCP'yi dener,
   `netclone-client`'ı başlatır.
7. Ajan: `/api/client/info` → disk seçimi → `/api/client/register` →
   `/api/client/plan` (1,5 sn'de bir) → `start` gelince transfer.

## HTTP API

| Metot | Yol | Açıklama |
|---|---|---|
| GET | `/api/state` | panelin ana durumu (yakalama, oturum, loglar, arayüzler) |
| GET | `/api/disks` | tam diskler (model, boyut, şema, boot medyası işareti) |
| GET | `/api/mounts` | bağlı dosya sistemleri + boş alan |
| GET | `/api/partitions` | lsblk tabanlı disk/bölüm listesi |
| POST | `/api/mount` | `{device}` → `/media/<etiket>` altına bağlar |
| POST | `/api/unmount` | `{path}` |
| GET | `/api/images?dir=` | klasördeki NCI imajları + manifest |
| POST | `/api/capture` | `{source, destDir, name, level}` |
| POST | `/api/capture/cancel` | çalışan yakalamayı iptal eder |
| POST | `/api/session` | oturum açar: `{imagePath, expected, iface, dhcpMode, policy, portBase}` |
| POST | `/api/session/start` | sayacı beklemeden başlatır |
| POST | `/api/session/abort` | oturumu durdurur |
| POST | `/api/session/close` | biten oturumu temizler |
| GET | `/api/client/info?token=` | istemciye imaj boyutu/politika |
| POST | `/api/client/register` | istemci kaydı |
| GET | `/api/client/plan?token=&mac=` | `wait` / `start` / `abort` / `register` |
| POST | `/api/client/progress` | ilerleme |
| POST | `/api/client/result` | sonuç + checksum |
| GET | `/api/logs` | sunucu logları |

## Güvenlik modeli

- Panelde kimlik doğrulama **yoktur**; cihaz izole bir kurulum ağında çalışacak
  şekilde tasarlanmıştır. İnternete/açık ağa bağlamayın.
- İstemciler oturum token'ı ile doğrulanır; token her oturumda rastgele üretilir.
- Yakalama kaynağı, canlı sistemi barındıran disk olamaz; hedef disk ile aynı disk
  üzerine imaj yazılamaz; bağlı (mount) diskler yakalanamaz.
- Web arayüzü yalnızca yerel dosyaları sunar; `netboot/` dizini HTTP üzerinden
  okunabilir (çekirdek/initramfs), imajlar sunulmaz.

## Derleme hattı

```
build/Dockerfile            → live-build + Go araç zinciri
build/build-iso.sh          → Go binary'leri (CGO_ENABLED=0) + lb build
build/live/                 → live-build config ağacı
  auto/config               → lb config parametreleri
  config/package-lists/     → kurulacak paketler
  config/includes.chroot/   → servisler, lightdm, kiosk, dokümantasyon
  config/hooks/live/0100-*  → netboot hazırlığını tetikler
build/make-netboot-initramfs.sh → istemci initramfs'i (busybox + kmod + araçlar)
build/netboot/init          → istemci PID 1 betiği
```

### Netboot varlıklarının hazırlanması

`/usr/local/bin/netclone-prepare-assets` çekirdeği, GRUB/PXELINUX yükleyicilerini
ve istemci initramfs'ini `/usr/lib/netclone/netboot` altına yerleştirir. Hem
live-build hook'undan hem de `netclone.service`'in `ExecStartPre` adımından
çağrılır: live-build'in `includes.chroot` kopyalamasını hook'tan önce mi sonra mı
yaptığı garanti olmadığı için hazırlık **idempotent** ve boot'ta tekrar
çalışacak şekilde tasarlandı. Dosyalar zaten varsa hiçbir şey yapılmaz.
