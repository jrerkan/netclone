# Sorun giderme

## Panel / sistem

**Panel açılmıyor, ekran siyah**
Grafik sürücüsü yüklenmemiş olabilir. `Alt+F2` ile konsola geçin:

```bash
systemctl status netclone
ip -4 addr
```

Başka bir bilgisayardan `http://<sunucu-ip>:8080` adresini açın.

**"Sunucuya ulaşılamadı"**
`systemctl status netclone` çıktısına bakın. Port 8080 başka bir süreçte olabilir:

```bash
ss -lntp | grep 8080
journalctl -u netclone -n 100
```

## İmaj alma

**"disk ... is mounted; unmount it first"**
Kaynak diskin bölümleri bağlı. Panelde *Hedef konum → ilgili bağlantı → Ayır*,
veya konsolda `umount /dev/sda1`.

**"cannot store the image on the disk that is being captured"**
Hedef klasör, imajı alınacak disk üzerinde. Harici disk seçin.

**İmaj çok yavaş**
- USB 2.0 portu yerine USB 3.0 kullanın.
- Sıkıştırma seviyesini "Hızlı" yapın.
- Kaynak disk bozuk sektör içeriyorsa okuma yavaşlar; `dmesg` çıktısını kontrol edin.

**Yakalama yarıda kaldı**
`.part` dosyası silinir, `image.nci` oluşmaz. Diskte boş alan olduğundan emin olun
(NCI sıkıştırılmış olsa da kaynak diskin dolu kısmı kadar yer gerekir).

## PXE / ağ

**İstemci PXE'de takılıyor ("DHCP..." veya "TFTP")**
1. Sunucuda dnsmasq çalışıyor mu? `systemctl status netclone`, panelde oturum
   `istemci bekleniyor` aşamasında mı?
2. `/usr/lib/netclone/netboot` içinde `vmlinuz`, `initrd.img`, `grubnetx64.efi`
   ve `pxelinux.0` var mı? Yoksa:

   ```bash
   /usr/local/bin/netclone-prepare-assets
   ls -l /usr/lib/netclone/netboot
   ```

   (İlk açılışta bu dosyalar otomatik üretilir; üretim 1-2 dakika sürebilir.)
3. Aynı switch'te misiniz? VLAN ayrımı varsa çalışmaz.
4. Ağda başka DHCP var mı? Varsa panelde **proxyDHCP** modunu seçin.
5. Güvenlik duvarı: UDP 67, 69 ve 9000-9101 açık olmalı (canlı sistemde kapalıdır).

```bash
journalctl -u netclone -f      # dnsmasq logları burada
tail -f /run/netclone/dnsmasq.leases
```

**"clients stopped responding (timeout)"**
İstemciler kayıt oldu ama aktarım başlamadı. Genellikle:
- istemci `udp-receiver` başlatamadı (initramfs'te araç eksik),
- veya multicast switch tarafından engelleniyor (IGMP snooping / storm control).

Test: sunucuda `udp-sender --file <imaj> --portbase 9100 --min-receivers 1 --full-duplex --nopointopoint --nokbd`
komutunu elle çalıştırıp çıktısını izleyin.

**İstemci "geç kaldı"**
Multicast başladıktan sonra kayıt oldu. Oturumu kapatıp yeniden başlatın ve
tüm istemcileri aynı anda boot edin.

## Yazma / disk

**"no disk can hold the image"**
İstemcideki disk imajdan küçük. Panelde imaj boyutunu ve istemci diskini karşılaştırın.
Farklı donanım varsa "İmaja sığan en küçük disk" politikasını kullanın.

**"checksum mismatch"**
Aktarım bozuldu. Ajan otomatik olarak yeniden dener (`-retries`, varsayılan 2).
Sürekli oluyorsa ağ donanımını (kablo, switch, NIC) kontrol edin.

**İstemci açılmıyor (işlem sonrası)**
- GPT diski imajdan büyükse yedek başlık taşınır (`sgdisk -e`); hata loglarına bakın.
- Windows BitLocker etkinse imaj şifreli kalır.
- Legacy BIOS ↔ UEFI uyumsuzluğu: kaynak makine UEFI ise hedef de UEFI boot etmelidir.

## Loglar

| Yer | İçerik |
|---|---|
| Panel → Olay günlüğü | sunucu + dnsmasq + udp-sender |
| `journalctl -u netclone` | systemd servis logu |
| İstemci ekranı | initramfs + ajan çıktısı (konsol) |
| `/run/netclone/dnsmasq.conf` | üretilen DHCP/TFTP yapılandırması |

## Sıfırdan test

Tek makinede denemek için sanal makine kullanın:

1. Sanal makine 1: NetClone ISO'su ile boot (2 disk: sistem + boş hedef).
2. Sanal makine 2 ve 3: boş disk, "Network boot" açık, aynı host-only ağ.
3. Panelde oturum açın, beklenen sayıyı 2 girin, VM'leri PXE ile başlatın.
